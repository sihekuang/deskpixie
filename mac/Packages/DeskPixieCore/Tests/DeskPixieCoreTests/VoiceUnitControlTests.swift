import Foundation
import XCTest
@testable import DeskPixieCore

/// The VPIO unit's lifecycle (ai-mascot `crates/audio-macos/src/engine.rs` `control_loop`, which
/// has no tests in Rust): built on demand, stopped after 300 ms with the mic off and the speaker
/// idle, 1 s back-off after a failed build, and a real device change ends the conversation.
final class VoiceUnitControlTests: XCTestCase {
    final class FakeHandle: VoiceUnitHandle {
        var stopped = 0
        func stop() { stopped += 1 }
    }

    final class Rig {
        var src = AudioDevicesTests.mac()
        var saved: (input: String?, output: String?) = (nil, nil)
        var micOn = false
        var speakerIdle = true
        var failBuild: ((DevicePair) -> Bool) = { _ in false }
        var builds: [DevicePair] = []
        var handles: [FakeHandle] = []
        var discards = 0
        var mutes = 0
        var reports: [AudioFailure] = []
        var running: [Bool] = []
        var logs: [String] = []
        var control: VoiceUnitControl!

        struct BuildFailed: Error {}

        init() {
            control = VoiceUnitControl(
                source: { [unowned self] in self.src },
                build: { [unowned self] chosen in
                    self.builds.append(chosen)
                    if self.failBuild(chosen) { throw BuildFailed() }
                    let h = FakeHandle()
                    self.handles.append(h)
                    return (h, chosen)
                },
                hooks: VoiceUnitControl.Hooks(
                    saved: { [unowned self] in self.saved },
                    micOn: { [unowned self] in self.micOn },
                    speakerIdle: { [unowned self] in self.speakerIdle },
                    discardSpeaker: { [unowned self] in self.discards += 1; self.speakerIdle = true },
                    mute: { [unowned self] in self.mutes += 1; self.micOn = false },
                    report: { [unowned self] in self.reports.append($0) },
                    log: { [unowned self] in self.logs.append($0) },
                    runningChanged: { [unowned self] in self.running.append($0) }))
        }
    }

    let t0: TimeInterval = 100

    func testNothingIsBuiltUntilAudioIsNeeded() {
        let r = Rig()
        for i in 0..<10 { r.control.tick(now: t0 + Double(i)) }
        XCTAssertTrue(r.builds.isEmpty)
        XCTAssertFalse(r.control.isRunning)
    }

    func testAudioToPlayBuildsTheUnitOnce() {
        let r = Rig()
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.control.tick(now: t0 + 0.02)
        XCTAssertEqual(r.builds, [.systemDefaults])
        XCTAssertTrue(r.control.isRunning)
        XCTAssertEqual(r.running, [true])
    }

    func testTheMicStartingBuildsTheUnit() {
        let r = Rig()
        r.micOn = true
        r.control.micStarted(now: t0)
        r.control.tick(now: t0)
        XCTAssertEqual(r.builds.count, 1)
    }

    func testTheUnitStopsAfter300msWithTheMicOffAndTheSpeakerIdle() {
        let r = Rig()
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.speakerIdle = true
        r.control.tick(now: t0 + 0.02)  // idle from here
        r.control.tick(now: t0 + 0.30)
        XCTAssertTrue(r.control.isRunning, "only 280 ms idle")
        r.control.tick(now: t0 + 0.33)
        XCTAssertFalse(r.control.isRunning)
        XCTAssertEqual(r.handles[0].stopped, 1)
        XCTAssertEqual(r.running, [true, false])
    }

    func testTheMicOnOrPlaybackKeepsTheUnitRunning() {
        let r = Rig()
        r.micOn = true
        r.control.micStarted(now: t0)
        for i in 0..<20 { r.control.tick(now: t0 + Double(i) * 0.1) }
        XCTAssertTrue(r.control.isRunning, "the mic is on")
        r.micOn = false
        r.speakerIdle = false
        for i in 20..<40 { r.control.tick(now: t0 + Double(i) * 0.1) }
        XCTAssertTrue(r.control.isRunning, "the speaker is playing")
        r.control.tick(now: t0 + 4.0)
        r.speakerIdle = true
        r.control.tick(now: t0 + 4.1)
        r.control.tick(now: t0 + 4.2)
        r.micOn = true  // a blip of activity restarts the idle clock
        r.control.tick(now: t0 + 4.3)
        r.micOn = false
        r.control.tick(now: t0 + 4.35)
        r.control.tick(now: t0 + 4.6)
        XCTAssertTrue(r.control.isRunning)
        r.control.tick(now: t0 + 4.66)
        XCTAssertFalse(r.control.isRunning)
    }

    func testAStoppedUnitRestartsOnDemand() {
        let r = Rig()
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.speakerIdle = true
        r.control.tick(now: t0 + 0.1)
        r.control.tick(now: t0 + 0.5)
        XCTAssertFalse(r.control.isRunning)
        r.speakerIdle = false
        r.control.tick(now: t0 + 0.6)
        XCTAssertTrue(r.control.isRunning)
        XCTAssertEqual(r.builds.count, 2)
    }

    func testAFailedBuildIsReportedAndNotRetriedForOneSecond() {
        let r = Rig()
        r.failBuild = { _ in true }
        r.speakerIdle = false
        r.control.tick(now: t0)
        XCTAssertEqual(r.builds.count, 1)
        XCTAssertEqual(r.discards, 1, "no unit will play the queued audio")
        guard case .other(let msg)? = r.reports.first else { return XCTFail("\(r.reports)") }
        XCTAssertTrue(msg.hasPrefix("cannot start audio: "), msg)
        r.speakerIdle = false  // the pump refills from the engine
        r.control.tick(now: t0 + 0.5)
        r.control.tick(now: t0 + 0.99)
        XCTAssertEqual(r.builds.count, 1, "backing off")
        r.failBuild = { _ in false }
        r.control.tick(now: t0 + 1.0)
        XCTAssertEqual(r.builds.count, 2, "retried once the back-off expires")
        XCTAssertTrue(r.control.isRunning)
        XCTAssertEqual(r.reports.count, 1)
    }

    func testAMicStartDuringTheBackOffHearsTheFailureAtOnce() {
        let r = Rig()
        r.failBuild = { _ in true }
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.speakerIdle = false
        r.micOn = true
        r.control.micStarted(now: t0 + 0.3)
        XCTAssertEqual(r.reports.count, 2, "the new conversation is told")
        XCTAssertEqual(r.reports[0], r.reports[1])
        XCTAssertEqual(r.discards, 2)
        r.control.tick(now: t0 + 0.4)
        XCTAssertEqual(r.builds.count, 1, "the failure is reported; the next start retries")
    }

    func testAMicStartAfterTheBackOffBuilds() {
        let r = Rig()
        r.failBuild = { $0 != .systemDefaults ? true : r.builds.count < 2 }
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.micOn = true
        r.control.micStarted(now: t0 + 1.5)
        XCTAssertEqual(r.reports.count, 1, "no stale failure")
        r.control.tick(now: t0 + 1.5)
        XCTAssertTrue(r.control.isRunning)
    }

    func testAPinnedDeviceThatFailsFallsBackToTheDefaults() {
        let r = Rig()
        r.saved = ("USB-1", nil)
        r.failBuild = { $0.input != .systemDefault }
        r.micOn = true
        r.control.micStarted(now: t0)
        r.control.tick(now: t0)
        XCTAssertEqual(r.builds, [AudioDevicesTests.usbPinned(), .systemDefaults])
        XCTAssertTrue(r.control.isRunning)
        XCTAssertTrue(r.reports.isEmpty)
    }

    func testASavedDeviceThatIsNotConnectedUsesTheDefault() {
        let r = Rig()
        r.saved = ("unplugged", "BuiltInSpeaker")
        r.micOn = true
        r.control.micStarted(now: t0)
        r.control.tick(now: t0)
        XCTAssertEqual(r.builds, [DevicePair(input: .systemDefault, output: .pinned(uid: "BuiltInSpeaker", id: 11))])
        XCTAssertTrue(r.logs.contains("audio device unplugged (Input) isn't connected; using the default"), "\(r.logs)")
    }

    func testARealDeviceChangeEndsTheConversation() {
        let r = Rig()
        r.micOn = true
        r.control.micStarted(now: t0)
        r.control.tick(now: t0)
        r.src.defaultOut = 20  // AirPods connect
        var t = t0
        while t < t0 + 3 {  // settling: adopted as the baseline
            t += 0.02
            r.control.tick(now: t)
        }
        XCTAssertTrue(r.control.isRunning)
        XCTAssertTrue(r.reports.isEmpty)
        r.src.defaultIn = 20
        let changedAt = t
        while t < t0 + 6, r.control.isRunning {
            t += 0.02
            r.control.tick(now: t)
        }
        XCTAssertFalse(r.control.isRunning)
        // Seen on the next poll, confirmed on the one after (polls are 1 s apart).
        XCTAssertGreaterThanOrEqual(t - changedAt, 1.0)
        XCTAssertLessThanOrEqual(t - changedAt, 2.05)
        XCTAssertEqual(r.reports, [.deviceChanged])
        XCTAssertEqual(r.mutes, 1)
        XCTAssertEqual(r.discards, 1)
        XCTAssertEqual(r.handles[0].stopped, 1)
        r.control.tick(now: t + 1)
        XCTAssertEqual(r.builds.count, 1, "muted: no rebuild until the next conversation")
    }

    func testAPinnedDeviceUnpluggedEndsTheConversation() {
        let r = Rig()
        r.saved = ("USB-1", nil)
        r.micOn = true
        r.control.micStarted(now: t0)
        r.control.tick(now: t0)
        r.src.all.removeAll { $0.uid == "USB-1" }
        var t = t0
        while t < t0 + 6, r.control.isRunning {
            t += 0.02
            r.control.tick(now: t)
        }
        XCTAssertEqual(r.reports, [.deviceChanged])
        XCTAssertTrue(r.logs.contains("pinned Input device USB-1: id 30 → gone"), "\(r.logs)")
    }

    func testShutdownStopsTheUnit() {
        let r = Rig()
        r.speakerIdle = false
        r.control.tick(now: t0)
        r.control.shutdown()
        XCTAssertFalse(r.control.isRunning)
        XCTAssertEqual(r.handles[0].stopped, 1)
        r.control.shutdown()
        XCTAssertEqual(r.handles[0].stopped, 1)
    }
}
