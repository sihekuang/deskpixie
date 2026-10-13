import Foundation
import CDP

/// Single-producer / single-consumer lock-free Int16 ring.
///
/// Producer (pump thread): `write`, `flush`. Consumer (render callback): `read`.
/// Counters are monotonic: `written` (producer), `readPos` (consumer),
/// `flushTo` (producer; `flush()` sets it to `written`). `read` first skips
/// `readPos` forward to `flushTo`. Atomics come from C helpers in CDP because
/// `Synchronization.Atomic` requires macOS 15 and the target is macOS 14.
public final class PlaybackRing: @unchecked Sendable {
    private let capacity: Int
    private let buf: UnsafeMutablePointer<Int16>
    // Atomic slots: 0 written, 1 readPos, 2 flushTo, 3 played, 4 underruns, 5 rmsBits, 6 expecting
    private let slots: UnsafeMutablePointer<Int64>

    public init(capacity: Int) {
        precondition(capacity > 0)
        self.capacity = capacity
        buf = .allocate(capacity: capacity)
        buf.initialize(repeating: 0, count: capacity)
        slots = .allocate(capacity: 8)
        slots.initialize(repeating: 0, count: 8)
    }

    deinit {
        buf.deallocate()
        slots.deallocate()
    }

    /// When true, a read that must zero-fill counts as an underrun.
    public var expectingAudio: Bool {
        get { cdp_load(slots + 6) != 0 }
        set { cdp_store(slots + 6, newValue ? 1 : 0) }
    }

    /// Samples currently readable (excludes flushed data).
    public var buffered: Int {
        let start = max(cdp_load(slots + 1), cdp_load(slots + 2))
        return Int(max(0, cdp_load(slots) - start))
    }
    public var played: Int64 { cdp_load(slots + 3) }
    public var underruns: Int64 { cdp_load(slots + 4) }
    public var lastRMS: Float { Float(bitPattern: UInt32(truncatingIfNeeded: cdp_load(slots + 5))) }

    /// Producer side. Returns the number of samples accepted (may be < count when full).
    public func write(_ s: UnsafeBufferPointer<Int16>) -> Int {
        guard let src = s.baseAddress, s.count > 0 else { return 0 }
        let w = cdp_load(slots)
        // Conservative: space is judged from readPos (not flushTo) so the consumer
        // can never be reading a slot the producer is overwriting.
        let used = Int(w - cdp_load(slots + 1))
        let n = min(s.count, capacity - used)
        if n <= 0 { return 0 }
        let start = Int(w % Int64(capacity))
        let first = min(n, capacity - start)
        (buf + start).update(from: src, count: first)
        if n > first { buf.update(from: src + first, count: n - first) }
        cdp_store(slots, w + Int64(n))
        return n
    }

    /// Consumer side (real-time): no allocation, no locks. Returns samples copied
    /// from the ring; the remainder of `dst` is zero-filled.
    public func read(into dst: UnsafeMutableBufferPointer<Int16>) -> Int {
        guard let out = dst.baseAddress, dst.count > 0 else { return 0 }
        var r = cdp_load(slots + 1)
        let f = cdp_load(slots + 2)
        if r < f { r = f; cdp_store(slots + 1, r) }
        let w = cdp_load(slots)
        let n = min(dst.count, Int(w - r))
        var sumSq: Double = 0
        if n > 0 {
            let start = Int(r % Int64(capacity))
            let first = min(n, capacity - start)
            out.update(from: buf + start, count: first)
            if n > first { (out + first).update(from: buf, count: n - first) }
            for i in 0..<n {
                let v = Double(out[i]) / 32768.0
                sumSq += v * v
            }
            cdp_store(slots + 1, r + Int64(n))
            _ = cdp_add(slots + 3, Int64(n))
            let rms = Float((sumSq / Double(n)).squareRoot())
            cdp_store(slots + 5, Int64(rms.bitPattern))
        }
        if n < dst.count {
            (out + n).update(repeating: 0, count: dst.count - n)
            if cdp_load(slots + 6) != 0 { _ = cdp_add(slots + 4, 1) }
        }
        return n
    }

    /// Producer side: drop everything buffered. Safe while the consumer reads.
    public func flush() {
        cdp_store(slots + 2, cdp_load(slots))
    }
}
