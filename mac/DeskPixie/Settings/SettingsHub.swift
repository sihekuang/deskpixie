import DeskPixieCore
import Foundation
import os

/// Owns the one `SettingsModel` the Settings window shows, and binds recorded hotkeys.
///
/// Wiring (S2/F1, files this WP doesn't own):
/// - `EngineCoordinator.handle(.settingsChanged)` should call `SettingsHub.shared?.refresh()`,
///   so the window follows tray writes and hand edits while it is open.
/// - The `hotkey` event (a hand edit reloaded by the core) can reuse `SettingsHub.bind`:
///   on `false`, call `settings.hotkeyFailed {combo}` (progress.md ruling).
/// - F1 may move `shared` into `CompositionRoot`; nothing else holds it.
@MainActor
enum SettingsHub {
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "settings")

    /// The model, once the Settings scene has been built.
    private(set) static var shared: SettingsModel?

    /// The model for the app's engine, made on first use, with the hotkey rebind hook set.
    static func model(for composition: CompositionRoot) -> SettingsModel {
        if let shared { return shared }
        let model = SettingsModel(client: EngineSettingsClient(engine: composition.engine))
        model.rebindHotkey = { [weak composition] combo, previous in
            guard let composition else { return false }
            return bind(combo, previous: previous, composition: composition)
        }
        shared = model
        return model
    }

    /// Binds `combo` as the voice hotkey with the app's `CarbonHotkey`. `CarbonHotkey.start`
    /// drops the old binding first, so on failure `previous` is bound again. Returns whether
    /// `combo` is now bound.
    static func bind(_ combo: String, previous: String?, composition: CompositionRoot) -> Bool {
        let hotkey = composition.hotkey
        let start = { [weak coordinator = composition.coordinator] (c: HotkeyRecorder.CarbonCombo) throws in
            try hotkey.start(keyCode: c.keyCode, modifiers: c.modifiers) { coordinator?.talk() }
        }
        guard let wanted = HotkeyRecorder.carbon(combo) else {
            log.error("hotkey \(combo, privacy: .public) has no key this Mac can bind")
            return false
        }
        do {
            try start(wanted)
            composition.appState.hotkeyError = nil
            return true
        } catch {
            log.error("hotkey \(combo, privacy: .public) is in use: \(String(describing: error), privacy: .public)")
            if let previous, let old = HotkeyRecorder.carbon(previous) {
                do {
                    try start(old)
                } catch {
                    log.error("could not bind \(previous, privacy: .public) again")
                }
            }
            return false
        }
    }
}
