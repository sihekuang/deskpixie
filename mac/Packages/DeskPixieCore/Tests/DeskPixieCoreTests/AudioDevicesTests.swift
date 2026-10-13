import Foundation
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `crates/audio-macos/src/devices.rs` tests (15: 14 pure + 1 that reads
/// this Mac's hardware, skipped unless `DESKPIXIE_REAL_AUDIO=1`).
final class AudioDevicesTests: XCTestCase {
    struct Fake: AudioDeviceSource {
        var all: [AudioDevice]
        var defaultIn: UInt32?
        var defaultOut: UInt32?

        func devices() -> [AudioDevice] { all }
        func defaultDevice(_ dir: AudioDirection) -> UInt32? { dir == .input ? defaultIn : defaultOut }
    }

    static func dev(_ id: UInt32, _ uid: String, _ name: String, _ input: Bool, _ output: Bool,
                    bluetooth: Bool = false) -> AudioDevice {
        AudioDevice(id: id, uid: uid, name: name, bluetooth: bluetooth, input: input, output: output)
    }

    static func mac() -> Fake {
        Fake(all: [
            dev(10, "BuiltInMic", "MacBook Pro Microphone", true, false),
            dev(11, "BuiltInSpeaker", "MacBook Pro Speakers", false, true),
            dev(20, "AA-BB:input", "AirPods Pro", true, true, bluetooth: true),
            dev(30, "USB-1", "Yeti", true, true),
            dev(40, "CADefaultDeviceAggregate-1234-0", "CADefaultDeviceAggregate-1234-0", true, true),
            dev(41, "VPAUAggregateAudioDevice-0x1", "VPAUAggregateAudioDevice-0x1", true, true),
        ], defaultIn: 10, defaultOut: 11)
    }

    func test_devices_are_listed_by_direction_with_bluetooth_marked() {
        let src = Self.mac()
        XCTAssertEqual(AudioDevices.choices(src, .input), [
            DeviceChoice(value: "BuiltInMic", label: "MacBook Pro Microphone"),
            DeviceChoice(value: "AA-BB:input", label: "AirPods Pro (Bluetooth)"),
            DeviceChoice(value: "USB-1", label: "Yeti"),
        ])
        XCTAssertEqual(AudioDevices.choices(src, .output).map(\.label),
                       ["MacBook Pro Speakers", "AirPods Pro (Bluetooth)", "Yeti"])
    }

    func test_voice_processing_aggregates_are_never_offered() {
        let src = Self.mac()
        XCTAssertTrue(AudioDevices.isPrivateAggregate(src.all[4]))
        XCTAssertTrue(AudioDevices.isPrivateAggregate(src.all[5]))
        XCTAssertFalse(AudioDevices.isPrivateAggregate(src.all[0]))
        for dir in [AudioDirection.input, .output] {
            XCTAssertTrue(AudioDevices.usable(src, dir).allSatisfy { $0.id != 40 && $0.id != 41 }, "\(dir)")
        }
        // A user's own aggregate device is a normal choice.
        let own = Self.dev(50, "~:AMS2_Aggregate:0", "Studio Aggregate", true, true)
        XCTAssertFalse(AudioDevices.isPrivateAggregate(own))
    }

    func test_a_connected_saved_device_is_pinned() {
        let src = Self.mac()
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "USB-1"), .pinned(uid: "USB-1", id: 30))
        XCTAssertEqual(AudioDevices.pick(src, .output, saved: "AA-BB:input"), .pinned(uid: "AA-BB:input", id: 20))
    }

    func test_nothing_saved_or_a_missing_device_uses_the_default() {
        let src = Self.mac()
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: nil), .systemDefault)
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: ""), .systemDefault)
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "unplugged"), .systemDefault)
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "BuiltInSpeaker"), .systemDefault, "a speaker is not a mic")
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "CADefaultDeviceAggregate-1234-0"), .systemDefault)
    }

    func test_a_replugged_device_is_used_again() {
        var src = Self.mac()
        src.all.removeAll { $0.uid == "USB-1" }
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "USB-1"), .systemDefault)
        src.all.append(Self.dev(31, "USB-1", "Yeti", true, true))
        XCTAssertEqual(AudioDevices.pick(src, .input, saved: "USB-1"), .pinned(uid: "USB-1", id: 31))
    }

    func test_the_default_is_watched_only_when_following_it() {
        var src = Self.mac()
        let pinned = AudioDevices.pick(src, .input, saved: "USB-1")
        XCTAssertEqual(AudioDevices.watched(src, .input, .systemDefault), .systemDefault(10))
        XCTAssertEqual(AudioDevices.watched(src, .input, pinned), .pinned(30))
        src.defaultIn = 20
        XCTAssertEqual(AudioDevices.watched(src, .input, .systemDefault), .systemDefault(20))
        XCTAssertEqual(AudioDevices.watched(src, .input, pinned), .pinned(30), "the Mac's default doesn't matter")
        src.all.removeAll { $0.uid == "USB-1" }
        XCTAssertEqual(AudioDevices.watched(src, .input, pinned), .pinned(nil))
        src.all.append(Self.dev(31, "USB-1", "Yeti", true, true))
        XCTAssertEqual(AudioDevices.watched(src, .input, pinned), .pinned(31), "the UID is found again, under its new ID")
    }

    // MARK: UnitWatch

    static let poll: TimeInterval = 1
    static let settle = DeviceWatch<Int>.settle

    static func usbPinned() -> DevicePair {
        DevicePair(input: .pinned(uid: "USB-1", id: 30), output: .systemDefault)
    }

    static func seen(_ input: UInt32?) -> WatchedPair {
        WatchedPair(input: .pinned(input), output: .systemDefault(11))
    }

    func test_a_pinned_device_ignores_default_changes() {
        let t0: TimeInterval = 100
        let p = Self.poll, s = Self.settle
        var w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        for i in 1..<8 {
            XCTAssertFalse(w.changed(Self.seen(30), now: t0 + p * Double(i)) { _ in })
        }
        w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        let moved = WatchedPair(input: .pinned(30), output: .systemDefault(12))
        XCTAssertFalse(w.changed(Self.seen(30), now: t0 + s) { _ in })
        XCTAssertFalse(w.changed(moved, now: t0 + s + p) { _ in })
        XCTAssertTrue(w.changed(moved, now: t0 + s + p * 2) { _ in }, "the speaker follows the default")
    }

    func test_a_pinned_device_lost_while_settling_is_reported_after_settling_and_two_polls() {
        let t0: TimeInterval = 100
        let p = Self.poll, s = Self.settle
        var w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        var logs: [String] = []
        XCTAssertFalse(w.changed(Self.seen(nil), now: t0 + p) { logs.append($0) }, "settling")
        XCTAssertFalse(w.changed(Self.seen(nil), now: t0 + p * 2) { logs.append($0) })
        XCTAssertEqual(logs, ["pinned Input device USB-1: id 30 → gone"], "logged once, when first seen")
        XCTAssertFalse(w.changed(Self.seen(nil), now: t0 + s) { _ in }, "first sighting after settling")
        XCTAssertTrue(w.changed(Self.seen(nil), now: t0 + s + p) { _ in })
    }

    func test_a_one_poll_pinned_blip_is_ignored() {
        let t0: TimeInterval = 100
        let p = Self.poll, s = Self.settle
        var w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        XCTAssertFalse(w.changed(Self.seen(30), now: t0 + s) { _ in })
        XCTAssertFalse(w.changed(Self.seen(nil), now: t0 + s + p) { _ in })
        XCTAssertFalse(w.changed(Self.seen(30), now: t0 + s + p * 2) { _ in }, "back on the built ID")
        XCTAssertFalse(w.changed(Self.seen(30), now: t0 + s + p * 3) { _ in })
    }

    func test_a_pinned_device_replugged_with_a_new_id_is_reported() {
        let t0: TimeInterval = 100
        let p = Self.poll, s = Self.settle
        var w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        var logs: [String] = []
        XCTAssertFalse(w.changed(Self.seen(30), now: t0 + s) { logs.append($0) })
        XCTAssertFalse(w.changed(Self.seen(31), now: t0 + s + p) { logs.append($0) })
        XCTAssertTrue(w.changed(Self.seen(31), now: t0 + s + p * 2) { logs.append($0) }, "the unit still points at 30")
        XCTAssertEqual(logs, ["pinned Input device USB-1: id 30 → 31"])
        w = UnitWatch(used: Self.usbPinned(), current: Self.seen(30), now: t0)
        XCTAssertFalse(w.changed(Self.seen(nil), now: t0 + s + p) { _ in })
        XCTAssertFalse(w.changed(Self.seen(31), now: t0 + s + p * 2) { _ in }, "a different mismatch: start over")
        XCTAssertTrue(w.changed(Self.seen(31), now: t0 + s + p * 3) { _ in })
    }

    func test_a_followed_default_still_settles() {
        // AirPods: starting the unit moves the default input to the hands-free device.
        let t0: TimeInterval = 100
        let p = Self.poll
        let both = DevicePair.systemDefaults
        var w = UnitWatch(used: both, current: WatchedPair(input: .systemDefault(10), output: .systemDefault(11)), now: t0)
        let hfp = WatchedPair(input: .systemDefault(20), output: .systemDefault(11))
        XCTAssertFalse(w.changed(hfp, now: t0 + p) { _ in })
        for i in 2..<8 {
            XCTAssertFalse(w.changed(hfp, now: t0 + p * Double(i)) { _ in })
        }
    }

    // MARK: apply / build_with_fallback

    /// Records each device set; refuses the directions in `refuse`.
    final class FakeUnit: AudioDeviceTarget {
        var set: [(AudioDirection, UInt32)] = []
        var refuse: [AudioDirection] = []

        func setCurrentDevice(_ dir: AudioDirection, id: UInt32) throws {
            if refuse.contains(dir) { throw AudioStatusError(status: -10851) }
            set.append((dir, id))
        }
    }

    func test_pinned_devices_are_set_on_their_element() {
        var unit = FakeUnit()
        var logs: [String] = []
        let chosen = DevicePair(input: .pinned(uid: "USB-1", id: 30), output: .pinned(uid: "BuiltInSpeaker", id: 11))
        XCTAssertEqual(AudioDevices.apply(unit, chosen) { logs.append($0) }, chosen)
        XCTAssertEqual(unit.set.map(\.0), [.input, .output])
        XCTAssertEqual(unit.set.map(\.1), [30, 11])
        XCTAssertEqual(logs, ["using audio device USB-1 (Input)", "using audio device BuiltInSpeaker (Output)"])
        unit = FakeUnit()
        XCTAssertEqual(AudioDevices.apply(unit, .systemDefaults) { _ in }, .systemDefaults)
        XCTAssertTrue(unit.set.isEmpty, "the default needs no set")
    }

    func test_a_device_that_cant_be_set_falls_back_to_the_default() {
        let unit = FakeUnit()
        unit.refuse = [.input]
        var logs: [String] = []
        let chosen = DevicePair(input: .pinned(uid: "USB-1", id: 30), output: .pinned(uid: "BuiltInSpeaker", id: 11))
        let used = AudioDevices.apply(unit, chosen) { logs.append($0) }
        XCTAssertEqual(used, DevicePair(input: .systemDefault, output: .pinned(uid: "BuiltInSpeaker", id: 11)))
        XCTAssertEqual(logs[0], "can't use audio device USB-1 (Input): -10851; using the default")
    }

    struct Failed: Error, Equatable { let why: String }

    func test_a_failed_build_on_a_pinned_device_retries_on_the_defaults() throws {
        var tried: [DevicePair] = []
        var logs: [String] = []
        let r: Int = try AudioDevices.buildWithFallback(Self.usbPinned(), log: { logs.append($0) }) { c in
            tried.append(c)
            if c.input == .systemDefault { return 1 }
            throw Failed(why: "init failed")
        }
        XCTAssertEqual(r, 1)
        XCTAssertEqual(tried, [Self.usbPinned(), .systemDefaults])
        XCTAssertEqual(logs.count, 1)

        var tries = 0
        XCTAssertThrowsError(try AudioDevices.buildWithFallback(Self.usbPinned(), log: { _ in }) { _ -> Int in
            tries += 1
            throw Failed(why: tries == 1 ? "first" : "no audio")
        }) { XCTAssertEqual($0 as? Failed, Failed(why: "no audio"), "the retry's failure is reported") }
        XCTAssertEqual(tries, 2)

        tries = 0
        XCTAssertThrowsError(try AudioDevices.buildWithFallback(.systemDefaults, log: { _ in }) { _ -> Int in
            tries += 1
            throw Failed(why: "no audio")
        })
        XCTAssertEqual(tries, 1, "nothing pinned: no retry")
    }

    // MARK: Real hardware (Rust: #[ignore] lists_real_devices)

    /// Prints this Mac's devices: `DESKPIXIE_REAL_AUDIO=1 swift test --filter lists_real`.
    func test_lists_real_devices() throws {
        try XCTSkipUnless(ProcessInfo.processInfo.environment["DESKPIXIE_REAL_AUDIO"] == "1",
                          "reads this Mac's audio hardware")
        let src = CoreAudioDevices()
        for d in src.devices() {
            print(d)
            XCTAssertTrue(src.alive(id: d.id, uid: d.uid), "\(d.name) is alive")
            XCTAssertEqual(src.find(uid: d.uid), d.id)
        }
        print("inputs:", AudioDevices.choices(src, .input))
        print("outputs:", AudioDevices.choices(src, .output))
    }

    // MARK: Settings payload (not in Rust: the Go side owns "System Default")

    func test_settings_choices_are_sent_per_field_without_the_default() throws {
        let args = AudioDevices.settingsChoices(Self.mac())
        XCTAssertEqual(args.map(\.field), ["voice.inputDevice", "voice.outputDevice"])
        XCTAssertEqual(args[0].choices.first, DeviceChoice(value: "BuiltInMic", label: "MacBook Pro Microphone"))
        XCTAssertFalse(args.contains { $0.choices.contains { $0.value.isEmpty } }, "Go prepends System Default")
        let json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(args[1])) as? [String: Any]
        XCTAssertEqual(json?["field"] as? String, "voice.outputDevice")
        XCTAssertEqual((json?["choices"] as? [[String: String]])?.first, ["value": "BuiltInSpeaker", "label": "MacBook Pro Speakers"])
    }
}

/// Port of ai-mascot `crates/audio-macos/src/device_watch.rs` tests (5).
final class DeviceWatchTests: XCTestCase {
    static let poll: TimeInterval = 1
    static let settle = DeviceWatch<[Int]>.settle

    func test_steady_devices_never_change() {
        let t0: TimeInterval = 100
        var w = DeviceWatch([1, 2], now: t0)
        for i in 1..<10 {
            XCTAssertFalse(w.changed([1, 2], now: t0 + Self.poll * Double(i)))
        }
    }

    func test_a_switch_while_the_unit_settles_is_adopted() {
        // AirPods: starting the unit moves the default input to the HFP device.
        let t0: TimeInterval = 100
        var w = DeviceWatch([1, 2], now: t0)
        XCTAssertFalse(w.changed([7, 2], now: t0 + Self.poll))
        XCTAssertFalse(w.changed([7, 8], now: t0 + Self.poll * 2))
        for i in 3..<10 {
            XCTAssertFalse(w.changed([7, 8], now: t0 + Self.poll * Double(i)), "the settled devices are the baseline")
        }
    }

    func test_a_one_poll_blip_is_ignored() {
        let t0: TimeInterval = 100
        let s = Self.settle, p = Self.poll
        var w = DeviceWatch([1, 2], now: t0)
        XCTAssertFalse(w.changed([1, 2], now: t0 + s))
        XCTAssertFalse(w.changed([7, 2], now: t0 + s + p))
        XCTAssertFalse(w.changed([1, 2], now: t0 + s + p * 2))
        XCTAssertFalse(w.changed([1, 2], now: t0 + s + p * 3))
    }

    func test_a_lasting_change_after_settling_is_reported() {
        let t0: TimeInterval = 100
        let s = Self.settle, p = Self.poll
        var w = DeviceWatch([1, 2], now: t0)
        XCTAssertFalse(w.changed([1, 2], now: t0 + s))
        XCTAssertFalse(w.changed([5, 6], now: t0 + s + p), "first sighting: wait for confirmation")
        XCTAssertTrue(w.changed([5, 6], now: t0 + s + p * 2))
    }

    func test_a_change_must_hold_steady_to_count() {
        let t0: TimeInterval = 100
        let s = Self.settle, p = Self.poll
        var w = DeviceWatch([1, 2], now: t0)
        XCTAssertFalse(w.changed([5, 6], now: t0 + s + p))
        XCTAssertFalse(w.changed([7, 8], now: t0 + s + p * 2), "a different device: start over")
        XCTAssertTrue(w.changed([7, 8], now: t0 + s + p * 3))
    }
}
