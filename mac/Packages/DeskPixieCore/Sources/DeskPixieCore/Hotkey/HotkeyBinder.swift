import Foundation
import os

/// Registers one global hotkey (CarbonHotkey; a fake in tests). `start` drops the old
/// binding first.
@MainActor
public protocol HotkeyRegistrar: AnyObject {
    func start(keyCode: UInt32, modifiers: UInt32, onPress: @escaping @MainActor () -> Void) throws
    func stop()
}

extension CarbonHotkey: HotkeyRegistrar {}

/// The talk hotkey's binding rules (EngineCoordinator's hotkey glue, V-030): bind a stored
/// combo, roll back to the previous one when the new one is taken, and keep the Swift-local
/// tray line "Hotkey unavailable: <formatted combo>" while nothing could be bound.
@MainActor
public final class HotkeyBinder {
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "hotkey")

    private let registrar: any HotkeyRegistrar
    private let onPress: @MainActor () -> Void
    /// Formats combos in the tray line.
    public var platform: SettingsPlatform
    /// The combo bound right now (nil = none).
    public private(set) var boundCombo: String?
    /// The tray line while a wanted combo could not be bound and nothing replaced it.
    public private(set) var unavailableLine: String?

    public init(registrar: any HotkeyRegistrar, platform: SettingsPlatform = .macos,
                onPress: @escaping @MainActor () -> Void) {
        self.registrar = registrar
        self.platform = platform
        self.onPress = onPress
    }

    /// Launch: the stored combo (Rust binds config.hotkey at startup); a failure is a tray line.
    public func bindAtLaunch(_ combo: String) {
        if !bind(combo, previous: nil) {
            unavailableLine = LaunchSettings.hotkeyUnavailable(combo, platform: platform)
        }
    }

    /// Binds `combo`; on failure `previous` (default: the combo bound now) is bound again.
    /// Returns whether `combo` is now bound. Used at launch, for the `hotkey` event and by the
    /// Settings window's recorder.
    @discardableResult
    public func bind(_ combo: String, previous: String?) -> Bool {
        guard let wanted = HotkeyRecorder.carbon(combo) else {
            // Nothing was unbound: the old binding (if any) still holds.
            Self.log.error("hotkey \(combo, privacy: .public) has no key this Mac can bind")
            return false
        }
        let fallback = previous ?? boundCombo
        do {
            try registrar.start(keyCode: wanted.keyCode, modifiers: wanted.modifiers, onPress: onPress)
            boundCombo = combo
            unavailableLine = nil
            return true
        } catch {
            Self.log.error("hotkey \(combo, privacy: .public) unavailable: \(String(describing: error), privacy: .public)")
            boundCombo = nil
            if let fallback, fallback != combo, let old = HotkeyRecorder.carbon(fallback) {
                do {
                    try registrar.start(keyCode: old.keyCode, modifiers: old.modifiers, onPress: onPress)
                    boundCombo = fallback
                } catch {
                    Self.log.error("could not bind \(fallback, privacy: .public) again")
                }
            }
            return false
        }
    }

    /// `hotkey` event: the core reloaded a hand-edited combo. If it cannot be bound the old one
    /// stays and `reportFailure` tells the core (`settings.hotkeyFailed`) so it does not adopt
    /// it; with nothing bound at all, the tray line shows.
    public func changed(_ combo: String, reportFailure: (String) -> Void) {
        guard combo != boundCombo else { return }
        if bind(combo, previous: boundCombo) { return }
        reportFailure(combo)
        if boundCombo == nil {
            unavailableLine = LaunchSettings.hotkeyUnavailable(combo, platform: platform)
        }
    }

    public func stop() {
        registrar.stop()
    }
}
