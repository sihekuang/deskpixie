import Foundation
import XCTest
@testable import DeskPixieCore

/// AudioPump with the VoiceIO's `AudioGate`: ports of the ai-mascot
/// `crates/audio-macos/src/mic_pump.rs` tests that apply to Swift (2 of 9), plus the stall
/// clock only running while a unit is rendering.
///
/// Omitted from mic_pump.rs (7): `the_original_comes_before_its_filtered_copy`,
/// `an_enabled_filter_cleans_every_frame_before_it_is_sent`, `a_disabled_filter_is_never_called`,
/// `clear_resets_a_filter_that_saw_audio`, `the_switch_is_read_per_frame_and_switching_off_resets_the_filter`,
/// `the_filter_gets_ready_as_soon_as_it_is_switched_on_even_before_any_audio`,
/// `the_filters_failure_is_visible`. The noise filter and the unfiltered debug copy run in Go
/// (`core/audio` Cleaner, P15; `core/voice` recording, P2), which get raw 40 ms frames from
/// here; whole-frame framing is `AudioPumpTests.testTickPushesWholeFramesOnly`.
final class AudioPumpGateTests: XCTestCase {
    private func writeMic(_ r: SampleRing, _ n: Int, value: Int16 = 7) {
        let a = [Int16](repeating: value, count: n)
        _ = a.withUnsafeBufferPointer { r.write($0, timestamp: 1) }
    }

    private func rig() -> (FakeEngine, SampleRing, AudioGate, AudioPump) {
        let e = FakeEngine()
        let mic = SampleRing(capacity: 48_000)
        let gate = AudioGate()
        let pump = AudioPump(engine: e, ring: PlaybackRing(capacity: 48_000), mic: mic, gate: gate)
        return (e, mic, gate, pump)
    }

    func test_no_original_while_the_mic_is_off() {
        let (e, mic, _, pump) = rig()
        writeMic(mic, AudioPump.micFrameSamples)
        pump.tick()
        XCTAssertTrue(e.pushes.isEmpty)
        XCTAssertEqual(mic.available, 0, "discarded, not kept for later")
    }

    func test_the_mic_going_off_drops_audio_and_resets_the_filter_once() {
        let (e, mic, gate, pump) = rig()
        let frame = AudioPump.micFrameSamples
        gate.micOn = true
        writeMic(mic, frame + 5)
        pump.tick()
        XCTAssertEqual(e.pushes.count, 1)
        gate.micOn = false
        writeMic(mic, frame)
        pump.tick()
        pump.tick()
        XCTAssertEqual(e.pushes.count, 1)
        // The 5 leftover samples were dropped too: a fresh frame needs a whole frame of audio.
        gate.micOn = true
        writeMic(mic, frame - 1)
        pump.tick()
        XCTAssertEqual(e.pushes.count, 1)
    }

    func testWithoutAGateTheMicIsAlwaysPumped() {
        let e = FakeEngine()
        let mic = SampleRing(capacity: 48_000)
        let pump = AudioPump(engine: e, ring: PlaybackRing(capacity: 48_000), mic: mic)
        writeMic(mic, AudioPump.micFrameSamples)
        pump.tick()
        XCTAssertEqual(e.pushes.count, 1)
    }

    func testAudioWaitingForAUnitToStartIsNotAStall() {
        let e = FakeEngine()
        let clock = ManualClock()
        let ring = PlaybackRing(capacity: 48_000)
        let gate = AudioGate()
        let pump = AudioPump(engine: e, ring: ring, mic: SampleRing(capacity: 4800), gate: gate,
                             targetBufferedSamples: 4800, clock: { clock.now })
        e.queueAudio(4800)
        pump.tick()
        clock.now += 2_000_000_000  // a slow (Bluetooth) unit start
        pump.tick()
        XCTAssertEqual(ring.buffered, 4800, "no unit yet: nothing reads, nothing is stalled")
        gate.unitRunning = true
        pump.tick()
        clock.now += 1_100_000_000
        pump.tick()
        XCTAssertEqual(ring.buffered, 0, "a running unit that does not read is stalled")
    }
}
