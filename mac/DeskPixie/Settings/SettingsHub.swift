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
    /// The voice list's previews (#480), once the model is made; `voice.preview` events go here.
    private(set) static var previewModel: VoicePreviewModel?
    /// The previews the voice list shows (made with the model; a stand-in that does nothing
    /// before then, which the window never shows).
    static var previews: VoicePreviewModel { previewModel ?? VoicePreviewModel(client: NoPreviews()) }

    private struct NoPreviews: VoicePreviewClient {
        func preview(_ voice: String) throws -> String { throw DeskPixieEngineError.refused("Previews aren't ready yet.") }
        func stop() throws {}
    }

    /// The model for the app's engine, made on first use, with the hotkey rebind hook set.
    static func model(for composition: CompositionRoot) -> SettingsModel {
        if let shared { return shared }
        let model = SettingsModel(client: EngineSettingsClient(engine: composition.engine))
        model.keyStore = KeychainSecrets.forApp(bundleID: Bundle.main.bundleIdentifier)
        model.rebindHotkey = { [weak composition] combo, previous in
            guard let composition else { return false }
            return bind(combo, previous: previous, composition: composition)
        }
        // History (#461) lists the tray's sessions and its buttons act like the tray's rows; the
        // tray state is observed, so an open window redraws whenever the tray is read again.
        model.historySource = { [weak composition] in composition?.appState.core?.agent?.history ?? [] }
        // Off (or before the core answers) the core sends no sessions: say so, not "none".
        model.historyAvailable = { [weak composition] in composition?.appState.core?.agent?.enabled == true }
        model.performAction = { [weak composition] id in composition?.coordinator.perform(id) }
        previewModel = VoicePreviewModel(client: EngineVoicePreviewClient(engine: composition.engine))
        shared = model
        return model
    }

    /// Binds `combo` as the voice hotkey; on failure `previous` is bound again. Returns whether
    /// `combo` is now bound.
    static func bind(_ combo: String, previous: String?, composition: CompositionRoot) -> Bool {
        composition.coordinator.bindHotkey(combo, previous: previous)
    }
}
