import Foundation
import CDP

/// Single-producer / single-consumer lock-free Int16 ring.
///
/// Producer (pump thread): `write`, `flush`. Consumer (render callback): `read`; or, while
/// no unit is running, the VoiceIO's control queue: `discardAll`.
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
    /// The consumer's read position. It moves on any read that finds samples or skips
    /// flushed ones, so it shows the render callback is alive even when `played` cannot move.
    var readPosition: Int64 { cdp_load(slots + 1) }
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
        if n > 0 {
            let start = Int(r % Int64(capacity))
            let first = min(n, capacity - start)
            out.update(from: buf + start, count: first)
            if n > first { (out + first).update(from: buf, count: n - first) }
            cdp_store(slots + 1, r + Int64(n))
            _ = cdp_add(slots + 3, Int64(n))
            let rms = Self.rms(UnsafeBufferPointer(start: out, count: n))
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

    /// Everything written has been read (played or skipped by a flush): Rust `Counters::is_idle`.
    public var isIdle: Bool { buffered == 0 }

    /// Consumer side, for when no unit is left to read the ring (torn down or failed to build):
    /// drops everything queued, so playback reads idle and the conversation does not wait on
    /// audio nobody plays (Rust `speaker::discard_all`). Unlike `flush`, it frees the space at
    /// once. Returns the samples dropped; they do not count as played. Never call it while a
    /// render callback may run.
    @discardableResult
    public func discardAll() -> Int {
        let r = max(cdp_load(slots + 1), cdp_load(slots + 2))
        let w = cdp_load(slots)
        let n = Int(max(0, w - r))
        cdp_store(slots + 1, w)
        cdp_store(slots + 5, Int64(Float(0).bitPattern))
        return n
    }

    /// RMS loudness of Int16 samples, 0...1. Allocation-free.
    public static func rms(_ samples: UnsafeBufferPointer<Int16>) -> Float {
        guard !samples.isEmpty else { return 0 }
        var sumSq: Double = 0
        for s in samples {
            let v = Double(s) / 32768.0
            sumSq += v * v
        }
        return Float((sumSq / Double(samples.count)).squareRoot())
    }

    public static func rms(_ samples: [Int16]) -> Float {
        samples.withUnsafeBufferPointer { rms($0) }
    }
}

/// The app's ring sizes (ai-mascot engine.rs): the speaker ring holds 120 s at 24 kHz so a
/// whole answer fits without back-pressure; the mic ring holds 2 s.
public enum VoiceAudioSizes {
    public static let speakerRingSamples = 24_000 * 120
    public static let micRingSamples = 24_000 * 2
}
