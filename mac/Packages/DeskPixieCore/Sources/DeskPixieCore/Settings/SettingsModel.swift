import Foundation
import Observation
import os

/// The Settings window's state: the snapshot shown, per-field errors and refused drafts, the
/// notice line, key status, and the commit rules of ai-mascot's native window
/// (`native/window.rs` `commit`, `reset_position`, `restart`, hotkey recorder). The SwiftUI
/// views only read it and call its methods.
@MainActor
@Observable
public final class SettingsModel {
    /// Binds `combo`; on failure the binder keeps (or restores) `previous`, the combo bound now.
    public typealias HotkeyBinder = @MainActor (_ combo: String, _ previous: String?) -> Bool

    /// The snapshot shown; nil until the first read succeeds.
    public private(set) var snapshot: SettingsSnapshot?
    /// Why the first read failed, while nothing is shown.
    public private(set) var loadError: String?
    public private(set) var secrets: SettingsSecrets?
    /// path → error shown under that field until its next good commit.
    public private(set) var errors: [String: String] = [:]
    /// path → refused text the field keeps.
    public private(set) var drafts: [String: String] = [:]
    /// path → text typed but not yet committed (text and number fields).
    public private(set) var editing: [String: String] = [:]
    /// A failed save or action; shown above the Restart bar, ahead of the snapshot's warning.
    public private(set) var notice: String?
    /// The hotkey field being recorded, if any.
    public var recordingPath: String?
    /// The projects folder checked on disk (#216); nil until read, or when the read failed.
    public private(set) var projectsFolder: ProjectsFolderStatus?
    /// The search field's text (#287); while it isn't blank the sidebar lists `hits`.
    public var query = "" {
        didSet { if query != oldValue { search() } }
    }
    /// What `query` matches, best first; empty while it is blank or the search failed.
    public private(set) var hits: [SettingsHit] = []
    /// The row to show and point out: a search result picked, or one Pixie was asked for.
    public private(set) var revealed: SettingsHit?
    /// Counts reveals, so revealing the same row again points it out again.
    public private(set) var revealCount = 0
    /// The last reveal a page pointed out. Kept here, not in the view, so reopening the window
    /// doesn't point out an old one again.
    @ObservationIgnored private var shownReveal = 0

    /// Where typed API keys are stored (the app sets the Keychain); nil = keys can't be typed.
    @ObservationIgnored public var keyStore: APIKeyStore?
    /// Key being typed per row; cleared once saved, so the field never shows a stored key.
    public private(set) var keyDrafts: [SettingsSecretID: String] = [:]
    /// The row's Keychain error ("Couldn't save to Keychain").
    public private(set) var keyErrors: [SettingsSecretID: String] = [:]
    /// Rows with a key in the store (they offer Remove).
    public private(set) var storedKeys: Set<SettingsSecretID> = []

    /// Binds a combo with the app's global hotkey before it is saved. Returns false when the
    /// shortcut can't be had (another app holds it, or the key can't be named). The app
    /// supplies it (see `DeskPixieApp`); when nil the combo is saved without binding.
    public var rebindHotkey: HotkeyBinder?

    @ObservationIgnored private let client: SettingsClient
    @ObservationIgnored private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "settings")

    public init(client: SettingsClient) {
        self.client = client
    }

    // MARK: Reading

    /// Controls are enabled only while settings.json can be read.
    public var enabled: Bool { snapshot?.fileError == nil }

    /// The window opened: read the snapshot and the key status.
    public func load() {
        reloadPixies()
        fetch { try client.snapshot() }
        readKeyStatus()
        refreshProjectsFolder()
    }

    /// The window became key: pick up hand edits, a key added to `.env` and a new pixie.
    public func focusGained() {
        reloadPixies()
        fetch { try client.reload() }
        readKeyStatus()
        refreshProjectsFolder()
    }

    /// A pixie added to your pixies folder joins the Pixie list before the snapshot is read
    /// (#266). A failure only means the list is as it was.
    private func reloadPixies() {
        try? client.reloadPixies()
    }

    /// The path that holds the projects folder setting.
    public static let projectsRootPath = "agent.projectsRoot"
    /// Workers' switch: the projects folder only matters while it is on.
    public static let workersPath = "agent.workers.enabled"

    /// Workers are on (the projects folder is in use).
    public var workersOn: Bool { snapshot?.values.at(Self.workersPath)?.boolValue ?? false }

    /// Reads the projects folder again (window shown, the setting saved, Check Again). Only
    /// while workers are on: with them off DeskPixie never looks at it, since a folder in
    /// ~/Documents can bring up a macOS privacy prompt (#216).
    public func refreshProjectsFolder() {
        projectsFolder = workersOn ? try? client.projectsFolder() : nil
    }

    /// Create: makes the missing projects folder; a failure is the notice.
    public func createProjectsFolder() {
        do {
            projectsFolder = try client.createProjectsFolder()
        } catch {
            notice = "Couldn't create the projects folder: \(Self.describe(error))"
        }
    }

    private func readKeyStatus() {
        secrets = try? client.secrets()
        storedKeys = Set(SettingsSecretID.allCases.filter { keyStore?.contains($0.variable) ?? false })
    }

    /// The core said settings changed (`settings.changed` event: tray write or hand edit).
    public func refresh() {
        fetch { try client.reload() }
    }

    private func fetch(_ read: () throws -> SettingsSnapshot) {
        do {
            show(try read())
            loadError = nil
        } catch {
            Self.log.error("settings read failed: \(String(describing: error), privacy: .public)")
            if snapshot == nil { loadError = Self.describe(error) }
        }
    }

    /// Takes `next` if it is newer than the one shown; an older one is dropped.
    public func show(_ next: SettingsSnapshot) {
        guard SettingsLayout.isNewer(snapshot?.seq, next.seq) else { return }
        var s = next
        s.setError = nil
        snapshot = s
        // A changed value can show or hide a result (visibleWhen).
        if searching { search() }
    }

    public func fields(in section: SettingsSection) -> [SettingsField] {
        guard let snapshot else { return [] }
        // The values shown: a picker saves as it changes, so switching the voice service or
        // GPT-Live delegation shows or hides the fields that depend on it at once.
        return SettingsLayout.fields(in: section, snapshot.fields, platform: snapshot.platform,
                                     values: snapshot.values)
    }

    public func spec(_ field: SettingsField) -> SettingsLayout.ControlSpec {
        SettingsLayout.controlSpec(field, values: snapshot?.values ?? [:], platform: snapshot?.platform ?? .macos,
                                   error: errors[field.path])
    }

    /// The control of the field at `path`, nil when the snapshot has no such field (the
    /// onboarding window shows single fields, #149).
    public func spec(path: String) -> SettingsLayout.ControlSpec? {
        snapshot?.fields.first { $0.path == path }.map(spec)
    }

    /// The text a text or number field shows: what is being typed, else a kept draft, else
    /// the stored value.
    public func text(for spec: SettingsLayout.ControlSpec) -> String {
        editing[spec.path] ?? drafts[spec.path] ?? Self.shownText(spec)
    }

    /// The text the window writes into the field from the snapshot.
    static func shownText(_ spec: SettingsLayout.ControlSpec) -> String {
        switch spec.control {
        case .text(let text, _): return text
        case .stepper(_, _, _, let text): return text
        case .path(let path, _, _): return path ?? ""
        default: return ""
        }
    }

    public func setEditing(_ path: String, _ text: String) {
        editing[path] = text
    }

    /// Banner, notice and Restart bar texts.
    public var bannerText: String? { snapshot?.fileError.map(SettingsLayout.bannerText) }
    public var noticeText: String? { notice ?? snapshot?.warning }
    public var restartText: String? {
        guard let snapshot else { return nil }
        return SettingsLayout.restartText(snapshot.restartPending, fields: snapshot.fields)
    }

    public func secretText(_ id: SettingsSecretID) -> String { SettingsLayout.secretText(secrets, id) }

    // MARK: API keys

    public func keyDraft(_ id: SettingsSecretID) -> String { keyDrafts[id] ?? "" }
    public func setKeyDraft(_ id: SettingsSecretID, _ text: String) { keyDrafts[id] = text }
    public func canSaveKey(_ id: SettingsSecretID) -> Bool { keyStore != nil && SettingsLayout.canSaveKey(keyDraft(id)) }
    public func hasStoredKey(_ id: SettingsSecretID) -> Bool { storedKeys.contains(id) }
    public func keyError(_ id: SettingsSecretID) -> String? { keyErrors[id] }

    /// Save: stores the typed key (trimmed), then hands the core the stored set and re-reads the
    /// status. A Keychain failure is the row's error and nothing is pushed; the typed key stays.
    public func saveKey(_ id: SettingsSecretID) {
        guard let keyStore, canSaveKey(id) else { return }
        let key = keyDraft(id).trimmingCharacters(in: .whitespacesAndNewlines)
        do {
            try keyStore.save(id.variable, key)
        } catch {
            Self.log.error("keychain save \(id.variable, privacy: .public) failed: \(String(describing: error), privacy: .public)")
            keyErrors[id] = SettingsLayout.keySaveError
            return
        }
        keyDrafts[id] = nil
        keyErrors[id] = nil
        pushKeys()
    }

    /// Remove: deletes the stored key, then hands the core what is left and re-reads the status.
    public func removeKey(_ id: SettingsSecretID) {
        guard let keyStore else { return }
        do {
            try keyStore.delete(id.variable)
        } catch {
            Self.log.error("keychain delete \(id.variable, privacy: .public) failed: \(String(describing: error), privacy: .public)")
            keyErrors[id] = SettingsLayout.keyRemoveError
            return
        }
        keyErrors[id] = nil
        pushKeys()
    }

    private func pushKeys() {
        guard let keyStore else { return }
        do {
            try client.setKeychain(keyStore.values(for: SettingsSecretID.allCases.map(\.variable)))
        } catch {
            notice = "Couldn't update the API keys: \(Self.describe(error))"
        }
        readKeyStatus()
    }

    // MARK: Search (#287)

    /// Whether the sidebar lists search results instead of the pages.
    public var searching: Bool { !query.trimmingCharacters(in: .whitespaces).isEmpty }

    /// Asks the core what `query` matches. A failure shows no results.
    private func search() {
        guard searching else {
            hits = []
            return
        }
        do {
            hits = try client.search(query)
        } catch {
            Self.log.error("settings.search failed: \(String(describing: error), privacy: .public)")
            hits = []
        }
    }

    /// Shows `hit`'s page and points it out.
    public func reveal(_ hit: SettingsHit) {
        recordingPath = nil
        revealed = hit
        revealCount += 1
    }

    /// The reveal `section`'s page has yet to point out, if any; taking it marks it pointed out.
    public func takeReveal(for section: SettingsSection) -> SettingsHit? {
        guard let hit = revealed, hit.section == section, revealCount != shownReveal else { return nil }
        shownReveal = revealCount
        return hit
    }

    // MARK: Committing

    /// Saves `input` for the field at `path`. A good save is shown; a refused one leaves its
    /// error under the field and the typed text in it. Returns whether it saved.
    @discardableResult
    public func commit(_ path: String, _ input: SettingsLayout.Input) -> Bool {
        guard let field = snapshot?.fields.first(where: { $0.path == path }) else { return false }
        let spec = self.spec(field)
        let typed = input.typed
        let value: JSONValue
        switch SettingsLayout.commitValue(spec, input) {
        case .success(let v): value = v
        case .failure(let e):
            errors[path] = e.message
            if let typed { drafts[path] = typed }
            editing[path] = nil
            return false
        }
        return send(path, value, typed: typed)
    }

    /// Sends a value that is already JSON; the shared tail of `commit` and `recordHotkey`.
    private func send(_ path: String, _ value: JSONValue, typed: String?) -> Bool {
        editing[path] = nil
        let result: SettingsSnapshot
        do {
            result = try client.set(path, value)
        } catch {
            notice = SettingsLayout.saveNotice(Self.describe(error))
            drafts[path] = nil
            return false
        }
        let refused = result.setError
        show(result)
        switch refused {
        case nil:
            errors[path] = nil
            drafts[path] = nil
            notice = nil
            if path == Self.projectsRootPath || path == Self.workersPath { refreshProjectsFolder() }
            return true
        case .invalid(let list)?:
            for placed in SettingsLayout.placeErrors(path, list, fields: snapshot?.fields ?? []) {
                errors[placed.path] = placed.message
            }
            if let typed { drafts[path] = typed }
            return false
        case .save(let message)?:
            notice = SettingsLayout.saveNotice(message)
            drafts[path] = nil
            return false
        }
    }

    /// Editing of a text or number field ended (Return or focus left): commit the typed text
    /// unless the field is as the window left it.
    @discardableResult
    public func commitText(_ path: String) -> Bool {
        guard let field = snapshot?.fields.first(where: { $0.path == path }) else { return true }
        let spec = self.spec(field)
        let shown = Self.shownText(spec)
        let typed = editing[path] ?? drafts[path] ?? shown
        guard SettingsLayout.shouldCommit(typed: typed, shown: shown, hasDraft: drafts[path] != nil,
                                          hasError: errors[path] != nil) else {
            editing[path] = nil
            return true
        }
        if case .path = spec.control {
            return commit(path, .path(typed))
        }
        return commit(path, .text(typed))
    }

    /// Commits every field with typed text (button clicks don't end editing). Returns false if
    /// any commit was refused.
    @discardableResult
    public func commitPendingEdits() -> Bool {
        var ok = true
        for path in editing.keys.sorted() where !commitText(path) {
            ok = false
        }
        return ok
    }

    /// A combo the recorder took: bind it with the app's hotkey first; only a bound combo is
    /// saved. If the save is refused, the old combo is bound again.
    @discardableResult
    public func recordHotkey(_ path: String, _ combo: String) -> Bool {
        recordingPath = nil
        let old = snapshot?.values.at(path)?.stringValue
        if let bind = rebindHotkey {
            guard bind(combo, old) else {
                errors[path] = SettingsLayout.hotkeyInUse
                return false
            }
        }
        let saved = commit(path, .hotkey(combo))
        if !saved, let bind = rebindHotkey, let old, old != combo, !bind(old, combo) {
            Self.log.error("could not bind the previous hotkey \(old, privacy: .public) again")
        }
        return saved
    }

    // MARK: Actions

    /// Reset Position: the core forgets the saved position (`position` = null).
    public func resetPosition() {
        commitPendingEdits()
        do {
            let result = try client.set("position", .null)
            show(result)
            if let e = result.setError {
                notice = "Couldn't reset the position: \(e.message)"
            } else {
                notice = nil
            }
        } catch {
            notice = "Couldn't reset the position: \(Self.describe(error))"
        }
    }

    public func openFile() {
        commitPendingEdits()
        do {
            try client.openFile()
        } catch {
            notice = Self.describe(error)
        }
    }

    /// Export Logs… (#225): the view picked where; the core writes the zip. True when it did.
    @discardableResult
    public func exportLogs(to path: String, app: String, os: String) -> Bool {
        do {
            try client.exportLogs(to: path, app: app, os: os)
            notice = nil
            return true
        } catch {
            notice = Self.describe(error)
            return false
        }
    }

    /// Restarts the coordinator (#179); the view has asked the user first.
    public func restartCoordinator() {
        do {
            try client.restartCoordinator()
        } catch {
            notice = Self.describe(error)
        }
    }

    /// Restart: commits any typed text first, and restarts only if that worked, so a typed
    /// value is never lost or half-applied. Returns whether the restart was asked for.
    @discardableResult
    public func restart() -> Bool {
        guard commitPendingEdits() else { return false }
        do {
            try client.restart()
            return true
        } catch {
            notice = Self.describe(error)
            return false
        }
    }

    static func describe(_ error: Error) -> String {
        if case DeskPixieEngineError.callFailed(let message) = error { return message }
        return String(describing: error)
    }
}
