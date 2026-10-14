import Foundation
import CDP

/// Single-producer / single-consumer lock-free Int16 ring for microphone audio.
///
/// Producer (input callback, real-time): `write`. Consumer (pump thread): `read`.
/// Same scheme as `PlaybackRing`: monotonic `written` / `readPos` counters in
/// atomic slots (C helpers in CDP; `Synchronization.Atomic` needs macOS 15).
/// When full, `write` drops the newest samples; `read` never zero-fills.
public final class SampleRing: @unchecked Sendable {
    private let capacity: Int
    private let buf: UnsafeMutablePointer<Int16>
    // Atomic slots: 0 written, 1 readPos, 2 newestTimestamp
    private let slots: UnsafeMutablePointer<Int64>

    public init(capacity: Int) {
        precondition(capacity > 0)
        self.capacity = capacity
        buf = .allocate(capacity: capacity)
        buf.initialize(repeating: 0, count: capacity)
        slots = .allocate(capacity: 4)
        slots.initialize(repeating: 0, count: 4)
    }

    deinit {
        buf.deallocate()
        slots.deallocate()
    }

    /// Samples ready to read.
    public var available: Int {
        Int(max(0, cdp_load(slots) - cdp_load(slots + 1)))
    }

    /// Timestamp (the producer's clock; Unix nanos in the app) of the newest sample written.
    public var newestTimestamp: UInt64 {
        UInt64(bitPattern: cdp_load(slots + 2))
    }

    /// Producer side (real-time safe). `timestamp` is the capture time of the last
    /// sample in `s`. Returns the number accepted; the excess (newest) is dropped.
    @discardableResult
    public func write(_ s: UnsafeBufferPointer<Int16>, timestamp: UInt64 = 0) -> Int {
        guard let src = s.baseAddress, s.count > 0 else { return 0 }
        let w = cdp_load(slots)
        let used = Int(w - cdp_load(slots + 1))
        let n = min(s.count, capacity - used)
        if n <= 0 { return 0 }
        let start = Int(w % Int64(capacity))
        let first = min(n, capacity - start)
        (buf + start).update(from: src, count: first)
        if n > first { buf.update(from: src + first, count: n - first) }
        cdp_store(slots + 2, Int64(bitPattern: timestamp))
        cdp_store(slots, w + Int64(n))
        return n
    }

    /// Consumer side. Copies up to `dst.count` samples; the rest of `dst` is untouched.
    public func read(into dst: UnsafeMutableBufferPointer<Int16>) -> Int {
        guard let out = dst.baseAddress, dst.count > 0 else { return 0 }
        let r = cdp_load(slots + 1)
        let n = min(dst.count, Int(cdp_load(slots) - r))
        if n <= 0 { return 0 }
        let start = Int(r % Int64(capacity))
        let first = min(n, capacity - start)
        out.update(from: buf + start, count: first)
        if n > first { (out + first).update(from: buf, count: n - first) }
        cdp_store(slots + 1, r + Int64(n))
        return n
    }

    /// Consumer side: discard everything currently readable.
    public func clear() {
        cdp_store(slots + 1, cdp_load(slots))
    }
}
