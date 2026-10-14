import XCTest
@testable import DeskPixieCore

/// Ports of ai-mascot `ui/settings/test/settings-core.test.js` (21 → 21) against the Swift
/// equivalents of the web page's pure helpers (`SettingsLayout`, `HotkeyRecorder`,
/// `SettingsModel`).
///
/// Omitted: `ui/settings/test/page.test.js` (6). Each checks the web page's own files
/// (index.html CSP and landmarks, settings.css platform blocks and forced-colors focus,
/// settings.js invoke names and its afterBlur render), which the native SwiftUI window does
/// not have. The one behaviour among them that carries over, "the renderer calls only the
/// settings commands", is covered by `SettingsModelTests.test_the_window_uses_only_the_settings_methods`.
///
/// Adapted: the web `hotkeyFromEvent` took DOM key codes; the Swift recorder takes macOS
/// virtual key codes, so each case uses the key's `kVK_*` value. Its `null` event and the
/// `isNewer(…, null)` case have no Swift equivalent (non-optional types) and are dropped from
/// those tests.
final class SettingsCoreTests: XCTestCase {
    typealias L = SettingsLayout
    private let all = SettingsPlatform.all

    private func field(_ path: String, _ section: SettingsSection, _ kind: SettingsKind,
                       _ platforms: [SettingsPlatform]? = SettingsPlatform.all) -> SettingsField {
        SettingsField(path: path, section: section, label: path, kind: kind, platforms: platforms)
    }

    private var terminalOptions: [SettingsOption] {
        [.init(value: "warp", label: "Warp", platforms: [.macos]),
         .init(value: "iterm", label: "iTerm", platforms: [.macos]),
         .init(value: "kitty", label: "kitty", platforms: all)]
    }

    private var kindsOptions: [SettingsOption] {
        [.init(value: "claude", label: "Claude Code", platforms: all),
         .init(value: "codex", label: "Codex", platforms: [.macos])]
    }

    func test_sections_come_in_the_spec_order_with_fields_filtered_by_platform() {
        let fields = [
            field("canvas.autoOpen", .canvas, .bool),
            field("hotkey", .mascot, .hotkey),
            field("macOnly", .mascot, .bool, [.macos]),
        ]
        XCTAssertEqual(L.tabs.map(\.section.rawValue), ["Mascot", "Voice", "Agent", "Workers", "Notifications", "Canvas", "Advanced"])
        XCTAssertEqual(SettingsSection.allCases.map(\.rawValue), L.tabs.map(\.section.rawValue))
        XCTAssertEqual(L.fields(in: .mascot, fields, platform: .linux).map(\.path), ["hotkey"])
        XCTAssertEqual(L.fields(in: .mascot, fields, platform: .macos).map(\.path), ["hotkey", "macOnly"])
        XCTAssertEqual(L.fields(in: .canvas, fields, platform: .linux).map(\.path), ["canvas.autoOpen"])
    }

    func test_availableOn_treats_a_missing_platforms_list_as_everywhere() {
        XCTAssertFalse(L.availableOn([.macos], .linux))
        XCTAssertTrue(L.availableOn(nil, .linux))
    }

    func test_choices_are_filtered_by_platform() {
        XCTAssertEqual(L.choiceItems(terminalOptions, platform: .macos, current: "warp").map(\.value),
                       ["warp", "iterm", "kitty"])
        XCTAssertEqual(L.choiceItems(terminalOptions, platform: .linux, current: "kitty").map(\.value), ["kitty"])
    }

    func test_a_current_value_not_available_here_is_kept_and_labelled_never_dropped() {
        let opts = L.choiceItems(terminalOptions, platform: .linux, current: "warp")
        XCTAssertEqual(opts.last, L.MenuItem(label: "Warp \(L.notAvailable)", value: "warp", enabled: false))
        XCTAssertEqual(L.notAvailable, "(not available on this platform)")
        let unknown = L.choiceItems(terminalOptions, platform: .macos, current: "ghostty")
        XCTAssertEqual(unknown.last, L.MenuItem(label: "ghostty \(L.notFound)", value: "ghostty", enabled: false))
    }

    func test_a_device_choice_shows_null_as_System_Default() {
        let mic = field("voice.inputDevice", .voice, .choice([
            .init(value: "", label: "System Default", platforms: all),
            .init(value: "BuiltInMic", label: "MacBook Pro Microphone", platforms: all),
            .init(value: "USB-1", label: "Saved microphone — not connected", platforms: all),
        ]), [.macos])
        let spec = { (v: JSONValue) in L.controlSpec(mic, values: ["voice": ["inputDevice": v]], platform: .macos) }
        guard case .menu(let items, let selected) = spec(.null).control else { return XCTFail() }
        XCTAssertEqual(items.map(\.label), ["System Default", "MacBook Pro Microphone", "Saved microphone — not connected"])
        XCTAssertEqual(selected.map { items[$0].label }, "System Default")
        guard case .menu(let usb, let usbSelected) = spec("USB-1").control else { return XCTFail() }
        XCTAssertEqual(usbSelected.map { usb[$0].value }, "USB-1")
        guard case .menu(_, let eight) = L.controlSpec(field("scale", .mascot, .int(min: 1, max: 16, menu: true)),
                                                         values: ["scale": 8], platform: .macos).control
        else { return XCTFail() }
        XCTAssertEqual(eight, 7, "menus of numbers still match")
    }

    func test_multi_choice_keeps_unavailable_selections_checked_but_disabled() {
        XCTAssertEqual(L.checkItems(kindsOptions, platform: .linux, current: ["claude", "codex"]), [
            .init(label: "Claude Code", value: "claude", checked: true, enabled: true),
            .init(label: "Codex \(L.notAvailable)", value: "codex", checked: true, enabled: false),
        ])
        XCTAssertEqual(L.checkItems(kindsOptions, platform: .macos, current: ["claude"]).map(\.checked), [true, false])
    }

    func test_toggling_one_choice_leaves_the_others_available_or_not_as_they_were() {
        XCTAssertEqual(L.toggleMulti(["codex"], "claude", on: true), ["codex", "claude"])
        XCTAssertEqual(L.toggleMulti(["codex", "claude"], "claude", on: false), ["codex"])
        XCTAssertEqual(L.toggleMulti(nil, "claude", on: true), ["claude"])
    }

    func test_pop_up_number_menus_list_every_value_size_uses_the_tray_names() {
        let opts = L.intItems(path: "scale", min: 1, max: 16)
        XCTAssertEqual(opts.count, 16)
        XCTAssertEqual(opts[7], L.MenuItem(label: "Large (×8)", value: 8, enabled: true))
        XCTAssertEqual(opts[0].label, "×1")
        XCTAssertEqual(L.intItems(path: "agent.workers.max", min: 1, max: 4).map(\.label), ["1", "2", "3", "4"])
    }

    func test_values_are_read_by_dotted_path() {
        let values: JSONValue = ["agent": ["workers": ["max": 3]], "hotkey": "ctrl+alt+Space"]
        XCTAssertEqual(values.at("agent.workers.max"), 3)
        XCTAssertEqual(values.at("hotkey"), "ctrl+alt+Space")
        XCTAssertNil(values.at("agent.nope.deeper"))
    }

    func test_text_shown_in_inputs() {
        XCTAssertEqual(L.textOf(.null), "")
        XCTAssertEqual(L.textOf(nil), "")
        XCTAssertEqual(L.textOf(48217), "48217")
        XCTAssertEqual(L.textOf("~/Documents/Projects"), "~/Documents/Projects")
    }

    func test_typed_numbers_must_be_whole_the_range_check_is_the_cores() {
        let port = L.controlSpec(field("agent.port", .advanced, .int(min: 1024, max: 65535, menu: false)),
                                 values: [:], platform: .macos)
        XCTAssertEqual(L.commitValue(port, .text(" 8080 ")), .success(8080))
        XCTAssertEqual(L.commitValue(port, .text("80")), .success(80))
        XCTAssertEqual(L.commitValue(port, .text("12.5")), .failure(.init("Enter a whole number")))
        XCTAssertEqual(L.commitValue(port, .text("")), .failure(.init("Enter a whole number")))
    }

    func test_optional_text_clears_to_null_required_text_cannot_be_empty() {
        let spec = { (f: SettingsField) in L.controlSpec(f, values: [:], platform: .macos) }
        let model = spec(field("voice.homeCity", .voice, .text(optional: true)))
        let session = spec(field("agent.session", .advanced, .text(optional: false)))
        let root = spec(field("agent.projectsRoot", .agent, .path(dir: true, optional: false)))
        XCTAssertEqual(L.commitValue(model, .text("   ")), .success(.null))
        XCTAssertEqual(L.commitValue(model, .text(" opus ")), .success("opus"))
        XCTAssertEqual(L.commitValue(session, .text("")), .failure(.init("Can't be empty")))
        XCTAssertEqual(L.commitValue(root, .path("~/code")), .success("~/code"))
    }

    func test_the_hotkey_recorder_needs_ctrl_alt_or_super_except_for_function_keys() {
        typealias H = HotkeyRecorder
        let space: UInt16 = 0x31, keyK: UInt16 = 0x28, f5: UInt16 = 0x60, digit1: UInt16 = 0x12
        XCTAssertEqual(H.record(keyCode: space, flags: H.control | H.option), .combo("ctrl+alt+Space"))
        XCTAssertEqual(H.record(keyCode: keyK, flags: H.shift | H.command), .combo("shift+super+KeyK"))
        XCTAssertEqual(H.record(keyCode: f5, flags: 0), .combo("F5"))
        XCTAssertEqual(H.record(keyCode: keyK, flags: 0), .refused, "a bare letter would hijack typing")
        XCTAssertEqual(H.record(keyCode: keyK, flags: H.shift), .refused, "shift alone would hijack capitals")
        XCTAssertEqual(H.record(keyCode: digit1, flags: H.shift), .refused)
        XCTAssertEqual(H.record(keyCode: f5, flags: H.shift), .combo("shift+F5"))
        XCTAssertEqual(H.record(keyCode: keyK, flags: H.option), .combo("alt+KeyK"))
        XCTAssertEqual(H.record(keyCode: 0x38, flags: H.shift), .ignored, "modifiers alone are not a shortcut")
        XCTAssertEqual(H.record(keyCode: 0x36, flags: H.command), .ignored)
    }

    func test_hotkeys_read_the_platform_way() {
        XCTAssertEqual(L.formatHotkey("ctrl+alt+Space", platform: .macos), "⌃⌥Space")
        XCTAssertEqual(L.formatHotkey("shift+super+KeyK", platform: .macos), "⇧⌘K")
        XCTAssertEqual(L.formatHotkey("ctrl+alt+Space", platform: .windows), "Ctrl+Alt+Space")
        XCTAssertEqual(L.formatHotkey("shift+super+Digit1", platform: .windows), "Shift+Win+1")
        XCTAssertEqual(L.formatHotkey("shift+super+KeyK", platform: .linux), "Shift+Super+K")
        XCTAssertEqual(L.formatHotkey("F5", platform: .linux), "F5")
        XCTAssertEqual(L.formatHotkey("", platform: .macos), "")
    }

    func test_punctuation_and_arrow_keys_read_as_their_symbols() {
        XCTAssertEqual(L.formatHotkey("ctrl+alt+Minus", platform: .macos), "⌃⌥-")
        XCTAssertEqual(L.formatHotkey("super+Comma", platform: .macos), "⌘,")
        XCTAssertEqual(L.formatHotkey("ctrl+Period", platform: .windows), "Ctrl+.")
        XCTAssertEqual(L.formatHotkey("ctrl+Slash", platform: .linux), "Ctrl+/")
        XCTAssertEqual(L.formatHotkey("ctrl+Backquote", platform: .linux), "Ctrl+`")
        XCTAssertEqual(L.formatHotkey("alt+ArrowUp", platform: .macos), "⌥↑")
        XCTAssertEqual(L.formatHotkey("alt+ArrowLeft", platform: .windows), "Alt+Left")
        XCTAssertEqual(L.formatHotkey("ctrl+Space", platform: .windows), "Ctrl+Space")
    }

    func test_the_size_menu_is_picked_by_a_named_constant() {
        XCTAssertEqual(L.sizeField, "scale")
    }

    func test_a_registry_name_this_build_lacks_reads_as_not_available_here() {
        let audio = [SettingsOption(value: "macos-vpio", label: "macOS voice processing", platforms: [])]
        XCTAssertEqual(L.choiceItems(audio, platform: .linux, current: "macos-vpio"), [
            .init(label: "macOS voice processing \(L.notAvailable)", value: "macos-vpio", enabled: false),
        ])
    }

    func test_a_snapshot_older_than_the_one_shown_is_dropped() {
        XCTAssertTrue(L.isNewer(nil, 1))
        XCTAssertTrue(L.isNewer(5, 6))
        XCTAssertFalse(L.isNewer(5, 5))
        XCTAssertFalse(L.isNewer(5, 3))
    }

    func test_key_status_reads_Found_Missing_or_Unknown_for_both_providers() {
        XCTAssertEqual(SettingsSecretID.allCases.map(\.variable), ["OPENAI_API_KEY", "GEMINI_API_KEY"])
        let s = SettingsSecrets(openai: .found, gemini: .missing)
        XCTAssertEqual(L.secretText(s, .openAI), "Found")
        XCTAssertEqual(L.secretText(s, .gemini), "Missing")
        XCTAssertEqual(L.secretText(nil, .openAI), "Unknown")
    }

    func test_key_status_names_the_source() throws {
        func text(_ source: String) throws -> String {
            let json = #"{"openai":"found","gemini":"missing","sources":{"openai":"\#(source)","gemini":""}}"#
            return L.secretText(try JSONDecoder().decode(SettingsSecrets.self, from: Data(json.utf8)), .openAI)
        }
        XCTAssertEqual(try text("env"), "From environment")
        XCTAssertEqual(try text("keychain"), "From Keychain")
        XCTAssertEqual(try text("dotenv"), "From .env")
        XCTAssertEqual(try text(""), "Found", "an older core names no source")
        let s = try JSONDecoder().decode(SettingsSecrets.self, from: Data(#"{"openai":"found","gemini":"missing"}"#.utf8))
        XCTAssertEqual(L.secretText(s, .openAI), "Found")
        XCTAssertEqual(L.secretText(s, .gemini), "Missing")
    }

    func test_a_key_can_be_saved_only_when_something_is_typed() {
        XCTAssertFalse(L.canSaveKey(""))
        XCTAssertFalse(L.canSaveKey("  \n\t"))
        XCTAssertTrue(L.canSaveKey(" sk-fake "))
    }

    @MainActor
    func test_clearing_one_field_leaves_the_others() throws {
        let client = FakeSettingsClient()
        let model = SettingsModel(client: client)
        model.load()
        model.commit("scale", .menu(99))
        model.commit("agent.port", .text("x"))
        XCTAssertEqual(Set(model.errors.keys), ["scale", "agent.port"])
        model.commit("scale", .menu(7))
        XCTAssertEqual(model.errors, ["agent.port": "Enter a whole number"])
    }

    func test_absolute_paths_are_told_apart_for_the_picker_start_folder() {
        XCTAssertTrue(L.isAbsolutePath("/Users/me"))
        XCTAssertTrue(L.isAbsolutePath("C:\\Users\\me"))
        XCTAssertFalse(L.isAbsolutePath("~/Documents"))
        XCTAssertFalse(L.isAbsolutePath(nil))
    }
}
