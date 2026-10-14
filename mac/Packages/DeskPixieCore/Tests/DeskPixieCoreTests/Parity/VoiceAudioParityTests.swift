import AudioToolbox
import XCTest
@testable import DeskPixieCore

/// V-022: the VPIO numbers from ai-mascot `engine.rs`. The unit's on-demand start, 300 ms idle
/// stop and 1 s build back-off are driven by `VoiceUnitControl` (VoiceUnitControlTests); this
/// pins the constants and the ducking configuration the real unit is given.
final class VoiceAudioParityTests: XCTestCase {
    func testOtherAudioIsDuckedAtMinWithoutAdvancedDucking() {
        let duck = VPIOVoiceIO.otherAudioDucking
        XCTAssertEqual(duck.mDuckingLevel, .min)
        XCTAssertFalse(duck.mEnableAdvancedDucking.boolValue)
    }

    func testRingSizesAreTwoMinutesOfSpeakerAndTwoSecondsOfMic() {
        XCTAssertEqual(VoiceAudioSizes.speakerRingSamples, 24_000 * 120)
        XCTAssertEqual(VoiceAudioSizes.micRingSamples, 24_000 * 2)
    }

    func testMicFramesAreFortyMilliseconds() {
        XCTAssertEqual(AudioPump.micFrameSamples * 1000 / Int(AudioPump.sampleRate), 40)
    }

    func testIdleStopIs300msAndBuildBackOffIsOneSecond() {
        XCTAssertEqual(VoiceUnitControl.idleStop, 0.3)
        XCTAssertEqual(VoiceUnitControl.buildBackoff, 1)
    }
}
