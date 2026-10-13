import Foundation
import XCTest
@testable import DeskPixieCore

/// Scriptable CoreEngine: serves `audio` on pull (optionally with a pending flush)
/// and records pushes and playback reports.
final class FakeEngine: CoreEngine, @unchecked Sendable {
    struct Push: Equatable { var count: Int; var capturedAt: UInt64; var first: Int16 }
    struct Report: Equatable { var played: Int64; var buffered: Int; var underruns: Int64 }

    private let lock = NSLock()
    private var _pushes: [Push] = []
    private var _reports: [Report] = []
    private var audio: [Int16] = []
    private var pendingFlush = false
    var infiniteAudio = false

    var pushes: [Push] { lock.withLock { _pushes } }
    var reports: [Report] { lock.withLock { _reports } }

    func queueAudio(_ n: Int, value: Int16 = 1) { lock.withLock { audio += [Int16](repeating: value, count: n) } }
    /// Like the real engine: a flush discards what was queued before it.
    func requestFlush() { lock.withLock { pendingFlush = true; audio.removeAll() } }

    func configure(_ c: EngineConfig) throws {}
    func hotkey() {}
    func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool {
        lock.withLock { _pushes.append(Push(count: s.count, capturedAt: capturedAt, first: s.first ?? 0)) }
        return true
    }
    func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) {
        lock.withLock {
            let flush = pendingFlush
            pendingFlush = false
            if infiniteAudio {
                for i in 0..<into.count { into[i] = 1 }
                return (into.count, flush)
            }
            let n = min(into.count, audio.count)
            for i in 0..<n { into[i] = audio[i] }
            audio.removeFirst(n)
            return (n, flush)
        }
    }
    func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {
        lock.withLock { _reports.append(Report(played: played, buffered: buffered, underruns: underruns)) }
    }
    func micFailed(_ reason: String) {}
    func pollEvent() -> EngineEvent? { nil }
    func packJSON() -> Data? { nil }
    func shutdown() {}
}

final class ManualClock: @unchecked Sendable {
    var now: UInt64 = 1_000_000_000
}

final class AudioPumpTests: XCTestCase {
    private func writeMic(_ r: SampleRing, _ n: Int, ts: UInt64) {
        let a = (0..<n).map { Int16(truncatingIfNeeded: $0) }
        _ = a.withUnsafeBufferPointer { r.write($0, timestamp: ts) }
    }
    private func drain(_ r: PlaybackRing, _ n: Int) {
        var out = [Int16](repeating: 0, count: n)
        _ = out.withUnsafeMutableBufferPointer { r.read(into: $0) }
    }

    func testTickPushesWholeFramesOnly() {
        let e = FakeEngine()
        let mic = SampleRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: PlaybackRing(capacity: 48000), mic: mic)
        writeMic(mic, 2000, ts: 10_000_000_000)
        pump.tick()
        XCTAssertEqual(e.pushes.map(\.count), [960, 960])
        XCTAssertEqual(e.pushes.map(\.first), [0, 960])
        XCTAssertEqual(mic.available, 80)
    }

    func testFrameTimestampsCountBackFromNewestSample() {
        let e = FakeEngine()
        let mic = SampleRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: PlaybackRing(capacity: 48000), mic: mic)
        let t: UInt64 = 10_000_000_000
        writeMic(mic, 2000, ts: t)
        pump.tick()
        // Frame 1 ends 1040 samples before the newest; frame 2 ends 80 before it.
        XCTAssertEqual(e.pushes.map(\.capturedAt), [t - 1040 * 1_000_000_000 / 24000, t - 80 * 1_000_000_000 / 24000])
    }

    func testTickFillsRingToTarget() {
        let e = FakeEngine()
        e.infiniteAudio = true
        let ring = PlaybackRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 48000))
        pump.tick()
        XCTAssertEqual(ring.buffered, 4800)
        drain(ring, 1000)
        pump.tick()
        XCTAssertEqual(ring.buffered, 4800)
    }

    func testTickStopsWhenEngineRunsDry() {
        let e = FakeEngine()
        e.queueAudio(1000)
        let ring = PlaybackRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 48000))
        pump.tick()
        XCTAssertEqual(ring.buffered, 1000)
    }

    func testFlushFromEngineFlushesRing() {
        let e = FakeEngine()
        e.queueAudio(3000, value: 9)
        let ring = PlaybackRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 48000))
        pump.tick()
        XCTAssertEqual(ring.buffered, 3000)
        e.requestFlush()
        pump.tick()
        XCTAssertEqual(ring.buffered, 0)
        // Flush is applied before the samples delivered with it.
        e.requestFlush()
        e.queueAudio(100, value: 5)
        pump.tick()
        XCTAssertEqual(ring.buffered, 100)
        var out = [Int16](repeating: 0, count: 100)
        XCTAssertEqual(out.withUnsafeMutableBufferPointer { ring.read(into: $0) }, 100)
        XCTAssertEqual(out, [Int16](repeating: 5, count: 100))
    }

    func testSamplesTheRingRejectsAreKeptForTheNextTick() {
        let e = FakeEngine()
        e.queueAudio(6, value: 1)
        let ring = PlaybackRing(capacity: 6)
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 64), targetBufferedSamples: 6)
        pump.tick()
        XCTAssertEqual(ring.buffered, 6)
        drain(ring, 1)
        e.requestFlush()
        e.queueAudio(4, value: 4)
        // The ring flushes, but its flushed slots stay occupied until the consumer
        // reads, so only 1 of the 4 new samples fits; the other 3 must not be lost.
        pump.tick()
        XCTAssertEqual(ring.buffered, 1)
        drain(ring, 1)
        pump.tick()
        XCTAssertEqual(ring.buffered, 3)
        var out = [Int16](repeating: 0, count: 3)
        _ = out.withUnsafeMutableBufferPointer { ring.read(into: $0) }
        XCTAssertEqual(out, [4, 4, 4])
    }

    func testReportsPlaybackEveryTick() {
        let e = FakeEngine()
        e.queueAudio(500)
        let ring = PlaybackRing(capacity: 48000)
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 48000))
        pump.tick()
        drain(ring, 200)
        pump.tick()
        pump.tick()
        XCTAssertEqual(e.reports, [
            .init(played: 0, buffered: 500, underruns: 0),
            .init(played: 200, buffered: 300, underruns: 0),
            .init(played: 200, buffered: 300, underruns: 0),
        ])
    }

    func testExpectingAudioOnlyWithinWindowOfLastSamples() {
        let e = FakeEngine()
        let ring = PlaybackRing(capacity: 48000)
        let clock = ManualClock()
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 48000),
                             clock: { clock.now })
        pump.tick()
        XCTAssertFalse(ring.expectingAudio)
        e.queueAudio(100)
        pump.tick()
        XCTAssertTrue(ring.expectingAudio)
        clock.now += 150_000_000
        pump.tick()
        XCTAssertTrue(ring.expectingAudio)
        clock.now += 100_000_000
        pump.tick()
        XCTAssertFalse(ring.expectingAudio)
    }

    func testStartStopRunsTicksOnThread() {
        let e = FakeEngine()
        let pump = AudioPump(engine: e, ring: PlaybackRing(capacity: 48000), mic: SampleRing(capacity: 48000))
        pump.start()
        let deadline = Date().addingTimeInterval(2)
        while e.reports.count < 3 && Date() < deadline { usleep(5_000) }
        pump.stop()
        let n = e.reports.count
        XCTAssertGreaterThanOrEqual(n, 3)
        usleep(50_000)
        XCTAssertEqual(e.reports.count, n, "no ticks after stop")
    }
}
