import Foundation

/// What the app reads from `settings.snapshot` at launch, outside the pixie's own values:
/// the hotkey to bind and the audio devices to pin.
public struct LaunchSettings: Equatable, Sendable {
    /// config.rs default.
    public static let defaultHotkey = "ctrl+alt+Space"
    public static let defaults = LaunchSettings(hotkey: defaultHotkey, inputDevice: nil, outputDevice: nil,
                                                platform: .macos)

    public var hotkey: String
    /// Pinned CoreAudio UIDs; nil = the system default.
    public var inputDevice: String?
    public var outputDevice: String?
    public var platform: SettingsPlatform

    public init(hotkey: String, inputDevice: String?, outputDevice: String?, platform: SettingsPlatform) {
        self.hotkey = hotkey
        self.inputDevice = inputDevice
        self.outputDevice = outputDevice
        self.platform = platform
    }

    /// Lenient: an unreadable snapshot (or none, when the core does not serve it) gives the
    /// defaults; missing or empty values fall back one by one.
    public static func fromSnapshot(_ json: Data?) -> LaunchSettings {
        guard let json, let snap = try? JSONDecoder().decode(SettingsSnapshot.self, from: json) else {
            return defaults
        }
        func nonEmpty(_ path: String) -> String? {
            guard let s = snap.values.at(path)?.stringValue, !s.isEmpty else { return nil }
            return s
        }
        return LaunchSettings(hotkey: nonEmpty("hotkey") ?? defaultHotkey,
                              inputDevice: nonEmpty("voice.inputDevice"),
                              outputDevice: nonEmpty("voice.outputDevice"),
                              platform: snap.platform)
    }

    /// The Swift-local tray line when `combo` cannot be bound.
    public static func hotkeyUnavailable(_ combo: String, platform: SettingsPlatform) -> String {
        "Hotkey unavailable: \(SettingsLayout.formatHotkey(combo, platform: platform))"
    }
}

/// `settings.hotkeyFailed` args: Swift could not bind a combo the core reloaded (`hotkey` event).
public struct HotkeyFailedArgs: Encodable, Sendable {
    public let combo: String
    public init(combo: String) { self.combo = combo }
}

/// `audio.failed` args: the voice unit could not be built (not a permission problem).
public struct AudioFailedArgs: Encodable, Sendable {
    public let reason: String
    public init(reason: String) { self.reason = reason }
}
