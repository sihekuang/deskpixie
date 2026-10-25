import DeskPixieCore
import Foundation
import os

/// Owns the one `SettingsModel` the Settings window shows, and binds recorded hotkeys.
/// `EngineCoordinator` refreshes `shared` on the `settings.changed` event and binds hotkeys
/// the core reloads (`hotkey` event) itself.
@MainActor
enum SettingsHub {
    /// The model, once the Settings scene has been built.
    private(set) static var shared: SettingsModel?
    /// The About page's versions (#295), made with `shared`.
    private(set) static var about: AboutModel?

    /// The model for the app's engine, made on first use, with the hotkey rebind hook set.
    static func model(for composition: CompositionRoot) -> SettingsModel {
        if let shared { return shared }
        let model = SettingsModel(client: EngineSettingsClient(engine: composition.engine))
        model.keyStore = KeychainSecrets()
        model.rebindHotkey = { [weak composition] combo, previous in
            guard let composition else { return false }
            return bind(combo, previous: previous, composition: composition)
        }
        shared = model
        return model
    }

    /// The About page's model for the app's engine, made on first use.
    static func aboutModel(for composition: CompositionRoot) -> AboutModel {
        if let about { return about }
        let model = AboutModel(client: EngineSettingsClient(engine: composition.engine))
        about = model
        return model
    }

    /// Binds `combo` as the voice hotkey; on failure `previous` is bound again. Returns whether
    /// `combo` is now bound.
    static func bind(_ combo: String, previous: String?, composition: CompositionRoot) -> Bool {
        composition.coordinator.bindHotkey(combo, previous: previous)
    }
}
