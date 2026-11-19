import CoreAudio
import Foundation
import XCTest
@testable import DeskPixieCore

/// The real units on this Mac's hardware (#451): a speaker-only unit plays without opening the
/// mic, so macOS doesn't show it in use. Opt-in, like `test_lists_real_devices`:
/// `DESKPIXIE_REAL_AUDIO=1 swift test --filter SpeakerOnlyUnitTests` (plays a short silence).
final class SpeakerOnlyUnitTests: XCTestCase {
    /// Whether any process has the Mac's default mic running (what the menu bar's mic
    /// indicator reflects). Needs no mic permission.
    private func defaultMicRunning() throws -> Bool {
        let mic = try XCTUnwrap(CoreAudioDevices().defaultDevice(.input), "no default mic")
        var addr = AudioObjectPropertyAddress(mSelector: kAudioDevicePropertyDeviceIsRunningSomewhere,
                                              mScope: kAudioObjectPropertyScopeGlobal,
                                              mElement: kAudioObjectPropertyElementMain)
        var running: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        XCTAssertEqual(AudioObjectGetPropertyData(AudioObjectID(mic), &addr, 0, nil, &size, &running), noErr)
        return running != 0
    }

    private func build(withMic: Bool) throws -> VoiceUnitHandle {
        let ring = PlaybackRing(capacity: 24_000)
        let silence = [Int16](repeating: 0, count: 12_000)
        _ = silence.withUnsafeBufferPointer { ring.write($0) }
        return try VPIOUnit.build(.systemDefaults, withMic: withMic, playback: ring, gate: AudioGate(),
                                  onMic: { _, _ in }).unit
    }

    func test_a_speaker_only_unit_leaves_the_mic_off() throws {
        try XCTSkipUnless(ProcessInfo.processInfo.environment["DESKPIXIE_REAL_AUDIO"] == "1",
                          "plays through this Mac's audio hardware")
        try XCTSkipIf(defaultMicRunning(), "another app is using the mic")

        let speaker = try build(withMic: false)
        Thread.sleep(forTimeInterval: 0.3)
        XCTAssertFalse(try defaultMicRunning(), "playing an update turned the mic on")
        speaker.stop()

        // The probe sees the mic: the unit with the mic does open it.
        let voice = try build(withMic: true)
        Thread.sleep(forTimeInterval: 0.3)
        XCTAssertTrue(try defaultMicRunning())
        voice.stop()
    }
}
