import Foundation

/// The Mac's microphones and speakers: what Settings ▸ Voice offers, and which device a
/// conversation uses. Port of ai-mascot `crates/audio-macos/src/devices.rs`. The rules (which
/// devices are listed, their labels, what a saved choice resolves to) are pure over an
/// `AudioDeviceSource`, so they are tested with fake devices; `CoreAudioDevices` is the real one.
public enum AudioDirection: String, Sendable, CaseIterable {
    case input = "Input"
    case output = "Output"
}

/// One audio device as CoreAudio reports it.
public struct AudioDevice: Equatable, Sendable {
    /// The `AudioDeviceID`: valid until the device goes away.
    public var id: UInt32
    /// The device's UID: stable across replugs, so it is what settings.json stores.
    public var uid: String
    public var name: String
    public var bluetooth: Bool
    public var input: Bool
    public var output: Bool

    public init(id: UInt32, uid: String, name: String, bluetooth: Bool, input: Bool, output: Bool) {
        self.id = id
        self.uid = uid
        self.name = name
        self.bluetooth = bluetooth
        self.input = input
        self.output = output
    }

    func has(_ dir: AudioDirection) -> Bool { dir == .input ? input : output }
}

/// Where devices come from: CoreAudio, or fakes in tests.
public protocol AudioDeviceSource {
    func devices() -> [AudioDevice]
    /// The Mac's default device for `dir`.
    func defaultDevice(_ dir: AudioDirection) -> UInt32?
    /// The connected device with this UID.
    func find(uid: String) -> UInt32?
    /// Whether device `id` is still alive and still the one with this UID (a cheap check).
    func alive(id: UInt32, uid: String) -> Bool
}

public extension AudioDeviceSource {
    func find(uid: String) -> UInt32? { devices().first { $0.uid == uid }?.id }
    func alive(id: UInt32, uid: String) -> Bool { find(uid: uid) == id }
}

/// One entry of a Settings device menu (`settings.choices`): the UID and its label.
public struct DeviceChoice: Codable, Equatable, Sendable {
    public var value: String
    public var label: String

    public init(value: String, label: String) {
        self.value = value
        self.label = label
    }
}

/// The `settings.choices` arguments for one device field.
public struct DeviceChoicesArgs: Encodable, Equatable, Sendable {
    public var field: String
    public var choices: [DeviceChoice]
}

/// The device a conversation uses for one direction.
public enum DeviceInUse: Equatable, Sendable {
    /// The Mac's default (nothing saved, or the saved device isn't connected).
    case systemDefault
    /// The saved device, connected when the unit was built.
    case pinned(uid: String, id: UInt32)
}

/// The mic and the speaker a unit is built with.
public struct DevicePair: Equatable, Sendable {
    public var input: DeviceInUse
    public var output: DeviceInUse

    public init(input: DeviceInUse, output: DeviceInUse) {
        self.input = input
        self.output = output
    }

    public static let systemDefaults = DevicePair(input: .systemDefault, output: .systemDefault)

    subscript(dir: AudioDirection) -> DeviceInUse {
        get { dir == .input ? input : output }
        set { if dir == .input { input = newValue } else { output = newValue } }
    }
}

/// One direction as the device watcher sees it.
public enum WatchedDevice: Equatable, Sendable {
    /// Following the Mac's default: which device that is.
    case systemDefault(UInt32?)
    /// A pinned device: the `AudioDeviceID` its UID has now (nil: not connected). The Mac's
    /// default doesn't matter.
    case pinned(UInt32?)
}

public struct WatchedPair: Equatable, Sendable {
    public var input: WatchedDevice
    public var output: WatchedDevice

    public init(input: WatchedDevice, output: WatchedDevice) {
        self.input = input
        self.output = output
    }
}

/// Where a unit's mic or speaker can be pointed: the AudioUnit, or a fake in tests.
public protocol AudioDeviceTarget: AnyObject {
    func setCurrentDevice(_ dir: AudioDirection, id: UInt32) throws
}

/// A failed CoreAudio call, described by its OSStatus alone (as Rust's `{e:?}` logs it).
public struct AudioStatusError: Error, CustomStringConvertible, Equatable {
    public let status: Int32
    public var description: String { "\(status)" }
}

public enum AudioDevices {
    /// Aggregate devices VoiceProcessingIO creates for itself while it runs: never a user's choice.
    static let privateAggregates = ["CADefaultDeviceAggregate", "VPAUAggregateAudioDevice"]

    public static func isPrivateAggregate(_ d: AudioDevice) -> Bool {
        privateAggregates.contains { d.name.contains($0) || d.uid.contains($0) }
    }

    /// The devices that can be picked for `dir`, in CoreAudio's order.
    public static func usable(_ src: AudioDeviceSource, _ dir: AudioDirection) -> [AudioDevice] {
        src.devices().filter { $0.has(dir) && !isPrivateAggregate($0) }
    }

    /// "AirPods Pro (Bluetooth)": Bluetooth mics are slow to start, so the menu says which they are.
    public static func label(_ d: AudioDevice) -> String {
        d.bluetooth ? "\(d.name) (Bluetooth)" : d.name
    }

    /// Settings' choices for `dir`: (UID, label). "System Default" ("") is not included: the
    /// Go settings schema puts it first.
    public static func choices(_ src: AudioDeviceSource, _ dir: AudioDirection) -> [DeviceChoice] {
        usable(src, dir).map { DeviceChoice(value: $0.uid, label: label($0)) }
    }

    /// Both `settings.choices` calls: `voice.inputDevice` then `voice.outputDevice`.
    public static func settingsChoices(_ src: AudioDeviceSource) -> [DeviceChoicesArgs] {
        [DeviceChoicesArgs(field: "voice.inputDevice", choices: choices(src, .input)),
         DeviceChoicesArgs(field: "voice.outputDevice", choices: choices(src, .output))]
    }

    /// What a saved UID means now: that device when it is connected, else the default.
    public static func pick(_ src: AudioDeviceSource, _ dir: AudioDirection, saved: String?) -> DeviceInUse {
        guard let uid = saved, !uid.isEmpty,
              let d = usable(src, dir).first(where: { $0.uid == uid }) else { return .systemDefault }
        return .pinned(uid: d.uid, id: d.id)
    }

    public static func watched(_ src: AudioDeviceSource, _ dir: AudioDirection, _ used: DeviceInUse) -> WatchedDevice {
        switch used {
        case .systemDefault:
            return .systemDefault(src.defaultDevice(dir))
        case .pinned(let uid, let id):
            return .pinned(src.alive(id: id, uid: uid) ? id : src.find(uid: uid))
        }
    }

    /// The devices the unit uses, as the watcher polls them: a pinned device must keep its ID;
    /// a default one must stay the default.
    public static func watched(_ src: AudioDeviceSource, _ used: DevicePair) -> WatchedPair {
        WatchedPair(input: watched(src, .input, used.input), output: watched(src, .output, used.output))
    }

    /// Points `target` at each pinned device. One that can't be set falls back to the default
    /// (the unit keeps it), and the watcher then follows the default instead.
    public static func apply(_ target: AudioDeviceTarget, _ chosen: DevicePair, log: (String) -> Void) -> DevicePair {
        var used = chosen
        for dir in AudioDirection.allCases {
            guard case .pinned(let uid, let id) = chosen[dir] else { continue }
            do {
                try target.setCurrentDevice(dir, id: id)
                log("using audio device \(uid) (\(dir.rawValue))")
            } catch {
                log("can't use audio device \(uid) (\(dir.rawValue)): \(error); using the default")
                used[dir] = .systemDefault
            }
        }
        return used
    }

    /// Builds with `chosen`; if that fails while a device is pinned, retries once on the
    /// defaults. The error is the retry's when there was one.
    public static func buildWithFallback<T>(_ chosen: DevicePair, log: (String) -> Void,
                                            _ build: (DevicePair) throws -> T) throws -> T {
        do {
            return try build(chosen)
        } catch where chosen != .systemDefaults {
            log("can't start audio on the chosen devices (\(error)); trying the defaults")
            return try build(.systemDefaults)
        }
    }
}
