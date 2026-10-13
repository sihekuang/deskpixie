import Foundation
import CDP
import os

/// Shuttles audio between the AudioUnit rings and the Go engine on a dedicated
/// thread every 10 ms (port of ai-mascot's audio-macos control loop + mic_pump).
///
/// Each `tick`:
/// 1. drains the mic ring in whole 960-sample (40 ms) frames → `engine.pushAudio`,
///    stamping each frame with the capture time of its last sample;
/// 2. pulls playback from the engine until `ring.buffered >= target` (an engine
///    flush calls `ring.flush()` before writing the samples delivered with it).
///    Samples the ring refuses (its flushed slots are not yet consumed) are kept
///    and written first on the next tick. A flush is observed on the first tick
///    the ring sits below target, i.e. within ~one render quantum while playing;
///    `ring.expectingAudio` is true only while the engine returned samples within
///    the last `expectingWindowNanos`, so underruns count only mid-response;
/// 3. reports playback (`played`, `buffered`, `lastRMS`, `underruns`).
///
/// `tick` uses only preallocated buffers. It runs on the pump thread only
/// (or the test thread when the pump is not started).
public final class AudioPump: @unchecked Sendable {
    public static let micFrameSamples = 960
    public static let sampleRate: UInt64 = 24_000
    public static let periodNanos: UInt64 = 10_000_000
    public static let expectingWindowNanos: UInt64 = 200_000_000
    /// Buffered audio whose `played` count has not moved for this long means the output is
    /// dead (e.g. the unit stopped rendering after a device change).
    public static let stallNanos: UInt64 = 1_000_000_000

    /// docs/go-no-go.md relies on one line per flush (barge-in timing cross-check).
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "audio")

    private let engine: CoreEngine
    private let ring: PlaybackRing
    private let mic: SampleRing
    private let target: Int
    private let clock: @Sendable () -> UInt64

    private let micFrame: UnsafeMutableBufferPointer<Int16>
    private let pullBuf: UnsafeMutableBufferPointer<Int16>
    private var pendingStart = 0
    private var pendingCount = 0
    private var lastAudioAt: UInt64?
    private var lastReadPosition: Int64 = 0
    private var stallSince: UInt64?
    private var outputStalled = false

    // Atomic slot 0: running flag (1 while the thread should keep ticking).
    private let flags: UnsafeMutablePointer<Int64>
    private let lock = NSLock()
    private var thread: Thread?
    private var exited: DispatchSemaphore?

    public convenience init(engine: CoreEngine, ring: PlaybackRing, mic: SampleRing,
                            targetBufferedSamples: Int = 4800) {
        self.init(engine: engine, ring: ring, mic: mic, targetBufferedSamples: targetBufferedSamples,
                  clock: { DispatchTime.now().uptimeNanoseconds })
    }

    /// `clock` returns monotonic nanoseconds (injectable for tests).
    init(engine: CoreEngine, ring: PlaybackRing, mic: SampleRing,
         targetBufferedSamples: Int = 4800, clock: @escaping @Sendable () -> UInt64) {
        precondition(targetBufferedSamples > 0)
        self.engine = engine
        self.ring = ring
        self.mic = mic
        self.target = targetBufferedSamples
        self.clock = clock
        micFrame = .allocate(capacity: Self.micFrameSamples)
        micFrame.initialize(repeating: 0)
        pullBuf = .allocate(capacity: targetBufferedSamples)
        pullBuf.initialize(repeating: 0)
        flags = .allocate(capacity: 1)
        flags.initialize(to: 0)
    }

    deinit {
        stop()
        micFrame.deallocate()
        pullBuf.deallocate()
        flags.deallocate()
    }

    /// Starts the 10 ms pump thread. No-op if already running.
    public func start() {
        lock.lock()
        defer { lock.unlock() }
        guard thread == nil else { return }
        cdp_store(flags, 1)
        let done = DispatchSemaphore(value: 0)
        let t = Thread { [self] in  // the running thread keeps the pump alive until stop()
            self.run()
            done.signal()
        }
        t.name = "deskpixie.audio-pump"
        t.qualityOfService = .userInteractive
        thread = t
        exited = done
        t.start()
    }

    /// Stops the pump thread and waits for its last tick to finish.
    public func stop() {
        lock.lock()
        let t = thread
        let done = exited
        thread = nil
        exited = nil
        lock.unlock()
        guard let t else { return }
        cdp_store(flags, 0)
        if Thread.current != t { done?.wait() }
    }

    private func run() {
        var deadline = clock()
        while cdp_load(flags) != 0 {
            tick()
            deadline &+= Self.periodNanos
            let now = clock()
            if deadline > now {
                var ts = timespec(tv_sec: 0, tv_nsec: Int(deadline - now))
                nanosleep(&ts, nil)
            } else {
                deadline = now // fell behind: don't burst to catch up
            }
        }
    }

    /// One pump cycle (see type docs). Exposed for tests.
    public func tick() {
        pumpMic()
        pumpPlayback()
        checkOutputStall()
        engine.reportPlayback(played: ring.played, buffered: ring.buffered,
                              rms: ring.lastRMS, underruns: ring.underruns)
    }

    private func pumpMic() {
        let frame = Self.micFrameSamples
        while true {
            let avail = mic.available
            guard avail >= frame else { return }
            let newest = mic.newestTimestamp
            let n = mic.read(into: micFrame)
            let after = UInt64(avail - n) // samples captured after this frame's last one
            let capturedAt = newest &- after * 1_000_000_000 / Self.sampleRate
            _ = engine.pushAudio(UnsafeBufferPointer(micFrame), capturedAt: capturedAt)
        }
    }

    private func pumpPlayback() {
        let now = clock()
        writePending()
        while pendingCount == 0 {
            let room = target - ring.buffered
            guard room > 0 else { break }
            let want = min(room, pullBuf.count)
            let dst = UnsafeMutableBufferPointer(rebasing: pullBuf[0..<want])
            let (n, flush) = engine.pullAudio(into: dst)
            if flush {
                let dropped = ring.buffered
                ring.flush()
                Self.log.notice("playback flush: dropped \(dropped, privacy: .public) buffered samples, played \(self.ring.played, privacy: .public)")
            }
            guard n > 0 else { break }
            lastAudioAt = now
            pendingStart = 0
            pendingCount = min(n, want)
            writePending()
            if n < want { break } // engine has nothing more right now
        }
        if let last = lastAudioAt, now &- last <= Self.expectingWindowNanos {
            ring.expectingAudio = true
        } else {
            ring.expectingAudio = false
        }
    }

    /// Backstop for a dead output: while samples sit in the ring and the consumer's read
    /// position (≈ `played`) does not move for `stallNanos`, flush the ring, and keep flushing
    /// every tick until the render callback reads again. The engine then sees `buffered == 0`,
    /// its queue drains, and its turn can end. The read position (not `played`) detects the
    /// recovery: a revived consumer first skips the flushed samples, which moves it.
    private func checkOutputStall() {
        let pos = ring.readPosition
        if pos != lastReadPosition {
            lastReadPosition = pos
            stallSince = nil
            if outputStalled {
                outputStalled = false
                Self.log.notice("playback resumed after a stall")
            }
            return
        }
        if outputStalled {
            ring.flush()
            pendingCount = 0
            return
        }
        guard ring.buffered > 0 else {
            stallSince = nil
            return
        }
        let now = clock()
        guard let since = stallSince else {
            stallSince = now
            return
        }
        guard now &- since >= Self.stallNanos else { return }
        let dropped = ring.buffered + pendingCount
        ring.flush()
        pendingCount = 0
        outputStalled = true
        Self.log.error("playback stalled for \(Self.stallNanos / 1_000_000, privacy: .public) ms: flushed \(dropped, privacy: .public) samples, flushing until the output renders again")
    }

    private func writePending() {
        guard pendingCount > 0 else { return }
        let src = UnsafeBufferPointer(rebasing: pullBuf[pendingStart..<(pendingStart + pendingCount)])
        let w = ring.write(src)
        pendingStart += w
        pendingCount -= w
    }
}
