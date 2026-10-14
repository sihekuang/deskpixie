import XCTest
@testable import DeskPixieCore

/// A `SettingsClient` that keeps values in memory, numbers snapshots like the core, and
/// refuses what `refuse` says.
final class FakeSettingsClient: SettingsClient {
    var values: JSONValue = SettingsFixture.defaults
    var seq: UInt64 = 0
    var fileError: String?
    var warning: String?
    var restartPending: [String] = []
    var secretsResult = SettingsSecrets(openai: .found, gemini: .missing)
    /// path → error a set of that path gets.
    var refuse: [String: SettingsSetError] = [:]
    var setThrows: Error?
    var openFileThrows: Error?
    /// Every method called, in order.
    var calls: [String] = []
    var sets: [(field: String, value: JSONValue)] = []

    func next() -> SettingsSnapshot {
        seq += 1
        return SettingsSnapshot(platform: .macos, values: values, fields: SettingsFixture.fields,
                                restartPending: restartPending, fileError: fileError, warning: warning, seq: seq)
    }

    func snapshot() throws -> SettingsSnapshot {
        calls.append("settings.snapshot")
        return next()
    }

    func reload() throws -> SettingsSnapshot {
        calls.append("settings.reload")
        return next()
    }

    func set(_ field: String, _ value: JSONValue) throws -> SettingsSnapshot {
        calls.append("settings.set")
        sets.append((field, value))
        if let setThrows { throw setThrows }
        if let e = refuse[field] {
            var s = next()
            s.setError = e
            return s
        }
        values = Self.setting(values, field.split(separator: ".").map(String.init), value)
        return next()
    }

    func secrets() throws -> SettingsSecrets {
        calls.append("settings.secrets")
        return secretsResult
    }

    func openFile() throws {
        calls.append("settings.openFile")
        if let openFileThrows { throw openFileThrows }
    }

    func restart() throws {
        calls.append("settings.restart")
    }

    var keychainPushes: [[String: String]] = []
    func setKeychain(_ values: [String: String]) throws {
        calls.append("secrets.keychain")
        keychainPushes.append(values)
    }

    private static func setting(_ tree: JSONValue, _ keys: [String], _ value: JSONValue) -> JSONValue {
        guard let key = keys.first else { return value }
        guard case .object(var o) = tree else { return tree }
        o[key] = setting(o[key] ?? [:], Array(keys.dropFirst()), value)
        return .object(o)
    }
}

/// Swift-only tests of the window's commit rules (ai-mascot `native/window.rs` had no unit
/// tests for them: they were AppKit callbacks).
@MainActor
final class SettingsModelTests: XCTestCase {
    private var client: FakeSettingsClient!
    private var model: SettingsModel!

    override func setUp() async throws {
        client = FakeSettingsClient()
        model = SettingsModel(client: client)
        model.load()
    }

    func test_the_snapshot_decodes_from_the_cores_json() throws {
        let json = #"""
        {"platform":"macos","values":{"scale":8,"hotkey":"ctrl+alt+Space"},
         "fields":[{"path":"scale","section":"Pixie","label":"Size","help":"h",
                    "kind":{"type":"int","min":1,"max":16,"menu":true},"apply":"live","platforms":["macos"]}],
         "restartPending":["voice.model"],"fileError":null,"warning":"w","seq":42,
         "setError":{"kind":"invalid","errors":[{"path":"scale","message":"bad"}]}}
        """#
        let s = try JSONDecoder().decode(SettingsSnapshot.self, from: Data(json.utf8))
        XCTAssertEqual(s.seq, 42)
        XCTAssertEqual(s.values.at("scale"), 8)
        XCTAssertEqual(s.fields.first?.kind, .int(min: 1, max: 16, menu: true))
        XCTAssertEqual(s.restartPending, ["voice.model"])
        XCTAssertNil(s.fileError)
        XCTAssertEqual(s.warning, "w")
        XCTAssertEqual(s.setError, .invalid([.init(path: "scale", message: "bad")]))
        let save = try JSONDecoder().decode(SettingsSetError.self, from: Data(#"{"kind":"save","message":"disk full"}"#.utf8))
        XCTAssertEqual(save, .save("disk full"))
        XCTAssertEqual(SettingsFixture.fields.count, 38, "40 leaves less the 2 Managed ones")
        XCTAssertEqual(SettingsFixture.defaults.at("agent.session"), "deskpixie")
    }

    func test_set_args_encode_null_as_null() throws {
        struct Args: Encodable { let field: String; let value: JSONValue }
        let data = try JSONEncoder().encode(Args(field: "position", value: .null))
        XCTAssertEqual(String(decoding: data, as: UTF8.self).contains(#""value":null"#), true)
    }

    func test_the_window_uses_only_the_settings_methods() {
        model.focusGained()
        model.commit("scale", .menu(3))
        model.resetPosition()
        model.openFile()
        model.restart()
        XCTAssertEqual(Set(client.calls), ["settings.snapshot", "settings.reload", "settings.secrets", "settings.set",
                                           "settings.openFile", "settings.restart"])
    }

    func test_a_stale_snapshot_is_ignored() {
        let shown = model.snapshot!.seq
        var old = SettingsFixture.snapshot(seq: shown - 1, values: ["scale": 3])
        model.show(old)
        XCTAssertEqual(model.snapshot?.seq, shown)
        old.seq = shown + 1
        model.show(old)
        XCTAssertEqual(model.snapshot?.values.at("scale"), 3)
    }

    func test_a_good_commit_shows_the_new_snapshot() {
        XCTAssertTrue(model.commit("scale", .menu(3)))
        XCTAssertEqual(client.sets.last?.field, "scale")
        XCTAssertEqual(client.sets.last?.value, 4)
        XCTAssertEqual(model.snapshot?.values.at("scale"), 4)
        XCTAssertNil(model.snapshot?.setError)
    }

    func test_a_refused_commit_keeps_the_typed_text_and_shows_the_error_under_the_field() {
        client.refuse["agent.port"] = .invalid([.init(path: "agent.port", message: "Use a whole number from 1024 to 65535")])
        model.setEditing("agent.port", "80")
        XCTAssertFalse(model.commitText("agent.port"))
        XCTAssertEqual(model.errors["agent.port"], "Use a whole number from 1024 to 65535")
        XCTAssertEqual(model.drafts["agent.port"], "80")
        let field = SettingsFixture.view("agent.port")
        XCTAssertEqual(model.text(for: model.spec(field)), "80", "the refused text stays in the field")
        XCTAssertTrue(model.spec(field).note.isError)
        client.refuse = [:]
        model.setEditing("agent.port", "8080")
        XCTAssertTrue(model.commitText("agent.port"))
        XCTAssertNil(model.errors["agent.port"])
        XCTAssertNil(model.drafts["agent.port"])
        XCTAssertEqual(model.text(for: model.spec(field)), "8080")
    }

    func test_an_untouched_text_field_does_not_commit() {
        model.setEditing("agent.session", "deskpixie")
        XCTAssertTrue(model.commitText("agent.session"))
        XCTAssertTrue(client.sets.isEmpty)
    }

    func test_errors_for_other_fields_are_named_under_the_committed_one() {
        client.refuse["agent.enabled"] = .invalid([.init(path: "agent.terminal", message: "Not one of the choices")])
        model.commit("agent.enabled", .switch(false))
        XCTAssertEqual(model.errors["agent.terminal"], "Not one of the choices")
        XCTAssertEqual(model.errors["agent.enabled"], "Terminal: Not one of the choices")
    }

    func test_a_failed_save_is_a_notice_and_drops_the_draft() {
        client.refuse["agent.session"] = .save("disk full")
        model.setEditing("agent.session", "m2")
        model.commitText("agent.session")
        XCTAssertEqual(model.noticeText, "Couldn't save settings: disk full")
        XCTAssertNil(model.drafts["agent.session"])
        client.refuse = [:]
        model.commit("scale", .menu(3))
        XCTAssertNil(model.notice, "a good save clears the notice")
    }

    func test_a_dp_call_error_is_a_save_notice() {
        client.setThrows = DeskPixieEngineError.callFailed("unknown method settings.set")
        model.commit("scale", .menu(3))
        XCTAssertEqual(model.noticeText, "Couldn't save settings: unknown method settings.set")
    }

    func test_a_hotkey_is_bound_before_it_is_saved() {
        var bound: [String] = []
        model.rebindHotkey = { combo, _ in bound.append(combo); return true }
        XCTAssertTrue(model.recordHotkey("hotkey", "alt+super+Space"))
        XCTAssertEqual(bound, ["alt+super+Space"])
        XCTAssertEqual(client.sets.map(\.field), ["hotkey"])
        XCTAssertEqual(model.snapshot?.values.at("hotkey"), "alt+super+Space")
    }

    func test_a_shortcut_in_use_is_not_saved() {
        model.recordingPath = "hotkey"
        model.rebindHotkey = { _, previous in XCTAssertEqual(previous, "ctrl+alt+Space"); return false }
        XCTAssertFalse(model.recordHotkey("hotkey", "super+KeyK"))
        XCTAssertEqual(model.errors["hotkey"], "That shortcut is in use")
        XCTAssertTrue(client.sets.isEmpty, "settings.set is not called")
        XCTAssertNil(model.recordingPath)
        XCTAssertEqual(model.snapshot?.values.at("hotkey"), "ctrl+alt+Space")
    }

    func test_a_refused_hotkey_save_binds_the_old_one_again() {
        var bound: [String] = []
        model.rebindHotkey = { combo, _ in bound.append(combo); return true }
        client.refuse["hotkey"] = .save("disk full")
        XCTAssertFalse(model.recordHotkey("hotkey", "super+KeyK"))
        XCTAssertEqual(bound, ["super+KeyK", "ctrl+alt+Space"])
    }

    func test_reset_position_sets_position_to_null() {
        model.resetPosition()
        XCTAssertEqual(client.sets.last?.field, "position")
        XCTAssertEqual(client.sets.last?.value, .null)
        client.refuse["position"] = .save("disk full")
        model.resetPosition()
        XCTAssertEqual(model.noticeText, "Couldn't reset the position: disk full")
    }

    func test_restart_commits_typed_text_first_and_not_after_a_refusal() {
        client.refuse["agent.port"] = .invalid([.init(path: "agent.port", message: "bad")])
        model.setEditing("agent.port", "80")
        XCTAssertFalse(model.restart())
        XCTAssertFalse(client.calls.contains("settings.restart"))
        client.refuse = [:]
        model.setEditing("agent.port", "8080")
        XCTAssertTrue(model.restart())
        XCTAssertEqual(client.sets.last?.value, 8080)
        XCTAssertEqual(client.calls.last, "settings.restart")
    }

    func test_chrome_follows_the_snapshot() {
        client.fileError = "line 3"
        client.warning = "Size couldn't be applied now (x); it applies after a restart."
        client.restartPending = ["voice.provider", "voice.model"]
        model.focusGained()
        XCTAssertFalse(model.enabled, "a broken file disables the controls")
        XCTAssertEqual(model.bannerText, "settings.json has a syntax error; fix it or reset it (line 3)")
        XCTAssertEqual(model.noticeText, "Size couldn't be applied now (x); it applies after a restart.")
        XCTAssertEqual(model.restartText, "Restart to apply: Voice service, Model")
        XCTAssertEqual(model.secretText(.openAI), "Found")
        XCTAssertEqual(model.secretText(.gemini), "Missing")
    }

    func test_open_file_failure_is_a_notice() {
        client.openFileThrows = DeskPixieEngineError.callFailed("Couldn't open the settings file")
        model.openFile()
        XCTAssertEqual(model.noticeText, "Couldn't open the settings file")
    }

    func test_a_failed_first_read_is_reported() {
        final class Failing: SettingsClient {
            func snapshot() throws -> SettingsSnapshot { throw DeskPixieEngineError.callFailed("unknown method settings.snapshot") }
            func reload() throws -> SettingsSnapshot { try snapshot() }
            func set(_ field: String, _ value: JSONValue) throws -> SettingsSnapshot { try snapshot() }
            func secrets() throws -> SettingsSecrets { throw DeskPixieEngineError.notInitialized }
            func openFile() throws {}
            func restart() throws {}
            func setKeychain(_ values: [String: String]) throws {}
        }
        let m = SettingsModel(client: Failing())
        m.load()
        XCTAssertNil(m.snapshot)
        XCTAssertEqual(m.loadError, "unknown method settings.snapshot")
        XCTAssertEqual(m.secretText(.openAI), "Unknown")
    }

    // MARK: API keys (#59)

    func test_saving_a_key_stores_it_pushes_the_set_and_rereads_the_status() {
        let store = FakeKeyStore()
        model.keyStore = store
        model.load()
        XCTAssertFalse(model.canSaveKey(.openAI))
        model.setKeyDraft(.openAI, "  sk-fake-typed ")
        XCTAssertTrue(model.canSaveKey(.openAI))
        client.secretsResult = SettingsSecrets(openai: .found, gemini: .missing,
                                               sources: SettingsKeySources(openai: "keychain", gemini: ""))
        client.calls = []
        model.saveKey(.openAI)
        XCTAssertEqual(store.items, ["OPENAI_API_KEY": "sk-fake-typed"])
        XCTAssertEqual(client.keychainPushes.last, ["OPENAI_API_KEY": "sk-fake-typed"])
        XCTAssertEqual(client.calls, ["secrets.keychain", "settings.secrets"])
        XCTAssertEqual(model.keyDraft(.openAI), "", "the field never keeps the key")
        XCTAssertTrue(model.hasStoredKey(.openAI))
        XCTAssertFalse(model.hasStoredKey(.gemini))
        XCTAssertNil(model.keyError(.openAI))
        XCTAssertEqual(model.secretText(.openAI), "From Keychain")
    }

    func test_a_failed_save_says_so_and_pushes_nothing() {
        let store = FakeKeyStore()
        store.saveThrows = true
        model.keyStore = store
        model.setKeyDraft(.gemini, "g-fake")
        client.calls = []
        model.saveKey(.gemini)
        XCTAssertEqual(model.keyError(.gemini), "Couldn't save to Keychain")
        XCTAssertTrue(client.keychainPushes.isEmpty)
        XCTAssertFalse(client.calls.contains("secrets.keychain"))
        XCTAssertEqual(model.keyDraft(.gemini), "g-fake", "the typed key stays to retry")
    }

    func test_blank_input_saves_nothing() {
        let store = FakeKeyStore()
        model.keyStore = store
        model.setKeyDraft(.openAI, "   ")
        model.saveKey(.openAI)
        XCTAssertTrue(store.items.isEmpty)
        XCTAssertTrue(client.keychainPushes.isEmpty)
    }

    func test_removing_a_key_deletes_it_and_pushes_the_rest() {
        let store = FakeKeyStore()
        store.items = ["OPENAI_API_KEY": "sk-fake-a", "GEMINI_API_KEY": "g-fake-b"]
        model.keyStore = store
        model.load()
        XCTAssertTrue(model.hasStoredKey(.openAI))
        model.removeKey(.openAI)
        XCTAssertEqual(store.items, ["GEMINI_API_KEY": "g-fake-b"])
        XCTAssertEqual(client.keychainPushes.last, ["GEMINI_API_KEY": "g-fake-b"])
        XCTAssertFalse(model.hasStoredKey(.openAI))
    }
}

/// An in-memory `APIKeyStore`.
final class FakeKeyStore: APIKeyStore {
    var items: [String: String] = [:]
    var saveThrows = false
    struct Failed: Error {}

    func read(_ variable: String) throws -> String? { items[variable] }
    func contains(_ variable: String) -> Bool { items[variable] != nil }
    func save(_ variable: String, _ value: String) throws {
        if saveThrows { throw Failed() }
        items[variable] = value
    }
    func delete(_ variable: String) throws { items[variable] = nil }
}
