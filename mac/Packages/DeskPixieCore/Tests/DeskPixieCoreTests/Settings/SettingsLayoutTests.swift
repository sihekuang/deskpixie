import XCTest
@testable import DeskPixieCore

/// Ports of ai-mascot `src-tauri/src/settings/native/layout.rs` tests (19 → 19). The Rust
/// tests read the schema from `schema::FIELDS`; here it comes from the Go core's `Views`
/// output (`SettingsFixture`), which is what the window receives.
///
/// Adapted, not omitted:
/// - `every_field_has_a_tab_and_every_kind_maps_to_one_control`: Managed fields never reach
///   Swift (Go's `Views` leaves them out), so the Managed → no control half is checked as
///   "no position/canvas.window field in the snapshot".
/// - `commit_values_round_trip_through_the_services_parse`: `service::parse` lives in Go
///   (`core/settings`, ported by P1); here each committed value is checked against the JSON
///   shape that parse accepts unchanged for the field's kind (`parseAccepts`).
final class SettingsLayoutTests: XCTestCase {
    typealias L = SettingsLayout
    private let defaults = SettingsFixture.defaults

    private func spec(_ path: String, _ values: JSONValue) -> L.ControlSpec {
        SettingsFixture.spec(path, values)
    }

    private func view(_ path: String) -> SettingsField { SettingsFixture.view(path) }

    func test_tabs_follow_the_sections_with_their_symbols() {
        XCTAssertEqual(L.tabs.map(\.section), [.pixie, .voice, .agent, .workers, .notifications, .canvas, .advanced])
        XCTAssertEqual(L.tabs.map(\.title), ["Pixie", "Voice", "Coordinator", "Workers", "Notifications", "Canvas", "Advanced"])
        XCTAssertEqual(L.tabs.map(\.symbol),
                       ["face.smiling", "waveform", "sparkles", "person.2", "bell.badge", "rectangle.on.rectangle", "gearshape.2"])
    }

    func test_every_tab_has_its_own_tint_in_tab_order() {
        XCTAssertEqual(L.tabs.map(\.tint), [.pink, .purple, .orange, .indigo, .red, .teal, .gray])
        XCTAssertEqual(Set(L.tabs.map(\.tint)).count, L.tabs.count, "Each sidebar tile reads at a glance")
    }

    func test_every_field_has_a_tab_and_every_kind_maps_to_one_control() {
        let views = SettingsFixture.fields
        XCTAssertFalse(views.contains { $0.path == "position" || $0.path == "canvas.window" },
                       "Managed fields have no view, so no control")
        for v in views {
            let expected = L.controlKind(v.kind)
            XCTAssertEqual(L.controlSpec(v, values: defaults, platform: .macos).control.kind, expected, v.path)
            XCTAssertTrue(L.tabs.contains { $0.section == v.section }, v.path)
        }
    }

    func test_every_shown_field_is_in_exactly_one_tab_on_macos() {
        let views = SettingsFixture.fields
        let count = L.tabs.map { L.fields(in: $0.section, views, platform: .macos).count }.reduce(0, +)
        XCTAssertEqual(count, views.count)
        XCTAssertEqual(L.fields(in: .pixie, views, platform: .macos).map(\.path),
                       ["hotkey", "pack", "scale", "proximityFade.enabled", "proximityFade.radius"])
    }

    func test_labels_end_in_a_colon_and_keep_their_text_for_accessibility() {
        let s = spec("scale", defaults)
        XCTAssertEqual(s.label, "Size:")
        XCTAssertEqual(s.a11yLabel, "Size")
        XCTAssertEqual(s.help, "Whole-number sizes keep the pixel art crisp.")
        XCTAssertEqual(s.note.text, "Whole-number sizes keep the pixel art crisp.")
        XCTAssertFalse(s.note.isError)
        let bad = L.controlSpec(view("scale"), values: defaults, platform: .macos, error: "Use a whole number from 1 to 16")
        XCTAssertEqual(bad.note.text, "Use a whole number from 1 to 16")
        XCTAssertTrue(bad.note.isError)
    }

    func test_a_menu_int_is_a_pop_up_and_the_rest_are_steppers() {
        guard case .menu(let items, let selected) = spec("agent.workers.max", ["agent": ["workers": ["max": 3]]]).control
        else { return XCTFail("workers.max is a menu") }
        XCTAssertEqual(items.map(\.label), ["1", "2", "3", "4"])
        XCTAssertEqual(selected, 2)
        XCTAssertEqual(spec("agent.port", ["agent": ["port": 7777]]).control,
                       .stepper(min: 1024, max: 65535, value: 7777, text: "7777"))
    }

    func test_size_items_use_the_trays_names() {
        let items = L.intItems(path: "scale", min: 1, max: 16)
        XCTAssertEqual(items.count, 16)
        XCTAssertEqual(items[0].label, "×1")
        XCTAssertEqual(items[3].label, "Small (×4)")
        XCTAssertEqual(items[4].label, "×5")
        XCTAssertEqual(items[5].label, "Medium (×6)")
        XCTAssertEqual(items[7].label, "Large (×8)")
        XCTAssertEqual(items[11].label, "Huge (×12)")
        XCTAssertEqual(items[7].value, 8)
        guard case .menu(_, let selected) = spec("scale", ["scale": 5]).control else { return XCTFail() }
        XCTAssertEqual(selected, 4)
    }

    func test_a_value_not_offered_here_is_a_disabled_labelled_item() {
        guard case .menu(let items, let selected) = spec("agent.terminal", ["agent": ["terminal": "warp"]]).control
        else { return XCTFail() }
        XCTAssertEqual(items.map(\.label), ["Warp", "iTerm"])
        XCTAssertEqual(selected, 0)

        let linux = L.controlSpec(view("agent.terminal"), values: ["agent": ["terminal": "warp"]], platform: .linux)
        guard case .menu(let litems, let lselected) = linux.control else { return XCTFail() }
        XCTAssertEqual(litems, [L.MenuItem(label: "Warp (not available on this platform)", value: "warp", enabled: false)])
        XCTAssertEqual(lselected, 0, "the current value stays selected")

        guard case .menu(let uitems, _) = spec("agent.terminal", ["agent": ["terminal": "kitty"]]).control
        else { return XCTFail() }
        XCTAssertEqual(uitems.last, L.MenuItem(label: "kitty (not found)", value: "kitty", enabled: false))
    }

    func test_a_device_menu_shows_null_as_the_system_default() {
        let s = spec("voice.inputDevice", ["voice": ["inputDevice": nil]])
        guard case .menu(let items, let selected) = s.control else { return XCTFail("a device is a menu") }
        XCTAssertEqual(items.map(\.label), ["System Default", "MacBook Pro Microphone"])
        XCTAssertEqual(selected, 0)
        XCTAssertEqual(L.commitValue(s, .menu(0)), .success(.null), "System Default is stored as null")
        XCTAssertEqual(L.commitValue(s, .menu(1)), .success("BuiltInMic"))
        guard case .menu(_, let sel) = spec("voice.inputDevice", ["voice": ["inputDevice": "BuiltInMic"]]).control
        else { return XCTFail() }
        XCTAssertEqual(sel, 1)
        guard case .menu(let out, _) = spec("voice.outputDevice", ["voice": ["outputDevice": nil]]).control
        else { return XCTFail() }
        XCTAssertEqual(out.count, 1, "no speakers listed: only the default")
    }

    func test_worker_kinds_are_checkboxes_and_keep_an_unknown_one() {
        let s = spec("agent.workers.kinds", ["agent": ["workers": ["kinds": ["codex", "gpt"]]]])
        guard case .checks(let items, let current) = s.control else { return XCTFail() }
        XCTAssertEqual(current, ["codex", "gpt"])
        XCTAssertEqual(items.map(\.label), ["Claude Code", "Codex", "gpt (not found)"])
        XCTAssertEqual(items.map(\.checked), [false, true, true])
        XCTAssertEqual(items.map(\.enabled), [true, true, false])
        XCTAssertEqual(L.commitValue(s, .check(index: 0, on: true)), .success(["codex", "gpt", "claude"]))
        XCTAssertEqual(L.commitValue(s, .check(index: 1, on: false)), .success(["gpt"]))
    }

    func test_text_path_and_hotkey_controls_show_their_values() {
        XCTAssertEqual(spec("agent.session", ["agent": ["session": "deskpixie"]]).control,
                       .text(text: "deskpixie", optional: false))
        XCTAssertEqual(spec("voice.homeCity", ["voice": ["homeCity": nil]]).control, .text(text: "", optional: true))
        XCTAssertEqual(spec("agent.projectsRoot", ["agent": ["projectsRoot": "/Users/me/Projects"]]).control,
                       .path(path: "/Users/me/Projects", dir: true, optional: false))
        XCTAssertEqual(spec("voice.personaPath", ["voice": ["personaPath": nil]]).control,
                       .path(path: nil, dir: false, optional: true))
        XCTAssertEqual(spec("hotkey", ["hotkey": "alt+super+Space"]).control,
                       .hotkey(combo: "alt+super+Space", title: "⌥⌘Space"))
        XCTAssertEqual(spec("canvas.autoOpen", ["canvas": ["autoOpen": true]]).control, .switch(on: true))
    }

    // Not a Rust port: voice.model, voice.voice and agent.model are free text there; here they are
    // pop-ups of suggested values (#45). A value not offered stays selected as "(not found)".
    func test_model_and_voice_pickers_offer_the_providers_choices() {
        guard case .menu(let models, let m) = spec("voice.model", defaults).control,
              case .menu(let voices, let v) = spec("voice.voice", defaults).control else {
            return XCTFail("model and voice are pop-ups")
        }
        XCTAssertEqual(models.map(\.label), ["GPT Realtime", "GPT Realtime mini"])
        XCTAssertEqual(m.map { models[$0].value }, "gpt-realtime")
        XCTAssertEqual(voices.count, 10)
        XCTAssertEqual(v.map { voices[$0].value }, "marin")
        XCTAssertEqual(L.commitValue(spec("voice.voice", defaults), .menu(0)), .success("alloy"))
    }

    func test_a_custom_model_or_voice_shows_as_not_found_and_stays_selected() {
        let custom = spec("voice.model", ["voice": ["model": "gpt-realtime-2025-08-28"]])
        guard case .menu(let items, let selected) = custom.control else { return XCTFail("pop-up") }
        XCTAssertEqual(items.last, L.MenuItem(label: "gpt-realtime-2025-08-28 (not found)",
                                              value: "gpt-realtime-2025-08-28", enabled: false))
        XCTAssertEqual(selected, items.count - 1)
        XCTAssertEqual(L.commitValue(custom, .menu(items.count - 1)), .failure(.init(L.notAvailable)))
    }

    func test_the_agent_model_picker_maps_default_to_null() {
        let unset = spec("agent.model", ["agent": ["model": nil]])
        guard case .menu(let items, let selected) = unset.control else { return XCTFail("pop-up") }
        XCTAssertEqual(items.first, L.MenuItem(label: "Default (Claude Code decides)", value: .null, enabled: true))
        XCTAssertEqual(selected, 0)
        XCTAssertEqual(items.map(\.value).compactMap(\.stringValue),
                       ["opus", "sonnet", "haiku", "fable", "claude-opus-5-5", "claude-sonnet-5-5",
                        "claude-haiku-5-5", "claude-fable-5-1"])
        XCTAssertEqual(L.commitValue(unset, .menu(0)), .success(.null))
        guard case .menu(let other, let s) = spec("agent.model", ["agent": ["model": "claude-opus-4-1"]]).control
        else { return XCTFail("pop-up") }
        XCTAssertEqual(other.last?.label, "claude-opus-4-1 (not found)")
        XCTAssertEqual(s, other.count - 1)
    }

    func test_typed_text_is_parsed_like_the_web_page() {
        let port = spec("agent.port", defaults)
        XCTAssertEqual(L.commitValue(port, .text(" 8080 ")), .success(8080))
        XCTAssertEqual(L.commitValue(port, .text("-3")), .success(-3), "ranges are the service's job")
        XCTAssertEqual(L.commitValue(port, .text("12.5")), .failure(.init(L.whole)))
        XCTAssertEqual(L.commitValue(port, .text("")), .failure(.init(L.whole)))
        XCTAssertEqual(L.commitValue(port, .text("1e3")), .failure(.init(L.whole)))
        XCTAssertEqual(L.commitValue(port, .step(4000)), .success(4000))
        let model = spec("agent.session", defaults)
        XCTAssertEqual(L.commitValue(model, .text("  ")), .failure(.init(L.empty)))
        XCTAssertEqual(L.commitValue(model, .text(" marin ")), .success("marin"))
        let optional = spec("voice.homeCity", defaults)
        XCTAssertEqual(L.commitValue(optional, .text("")), .success(.null))
        let persona = spec("voice.personaPath", defaults)
        XCTAssertEqual(L.commitValue(persona, .path(nil)), .success(.null))
        XCTAssertEqual(L.commitValue(persona, .path("/a/b.md")), .success("/a/b.md"))
        let root = spec("agent.projectsRoot", defaults)
        XCTAssertEqual(L.commitValue(root, .path(nil)), .failure(.init(L.empty)))
        XCTAssertEqual(L.commitValue(model, .switch(true)), .failure(.init(L.wrongInput)))
    }

    func test_a_disabled_menu_item_cannot_be_committed() {
        let linux = L.controlSpec(view("agent.terminal"), values: ["agent": ["terminal": "warp"]], platform: .linux)
        XCTAssertEqual(L.commitValue(linux, .menu(0)), .failure(.init(L.notAvailable)))
        XCTAssertEqual(L.commitValue(linux, .menu(9)), .failure(.init(L.notAvailable)))
    }

    /// Whether Go's `parse` takes `v` unchanged for `kind`: the JSON type each kind stores.
    private func parseAccepts(_ kind: SettingsKind, _ v: JSONValue) -> Bool {
        switch (kind, v) {
        case (.bool, .bool), (.int, .int), (.hotkey, .string): return true
        case (.choice(let options), .null): return options.contains { $0.value.isEmpty }
        case (.choice, .string): return true
        case (.multiChoice, .array(let a)): return a.allSatisfy { $0.stringValue != nil }
        case (.text(let optional), .null), (.path(_, let optional), .null): return optional
        case (.text, .string(let s)), (.path, .string(let s)): return !s.isEmpty
        default: return false
        }
    }

    func test_commit_values_round_trip_through_the_services_parse() {
        let values: JSONValue = [
            "hotkey": "alt+super+Space", "pack": "bit", "scale": 8,
            "proximityFade": ["enabled": true, "radius": 120],
            "voice": ["provider": "openai-realtime", "audio": "macos-vpio", "model": "m", "voice": "v", "chimes": true,
                      "idleTimeoutSecs": 30, "personaPath": nil],
            "agent": ["enabled": true, "terminal": "warp", "model": nil, "warm": false, "narration": true, "holdOpenSecs": 10,
                      "projectsRoot": "/Users/me/P", "workers": ["enabled": false, "max": 2, "kinds": ["claude"]],
                      "sessionHost": "herdr", "session": "pixie", "herdrPath": nil, "port": 4000, "keepPanesOnQuit": false,
                      "permissionTimeoutSecs": 30, "notifyPerMinute": 6],
            "canvas": ["autoOpen": true, "keep": 50],
        ]
        for f in SettingsFixture.fields {
            let s = L.controlSpec(f, values: values, platform: .macos)
            let inputs: [L.Input]
            switch s.control {
            case .switch: inputs = [.switch(true), .switch(false)]
            case .menu(let items, _): inputs = items.indices.map { .menu($0) }
            case .stepper: inputs = [.step(5), .text("42")]
            case .checks(let items, _): inputs = items.indices.flatMap { [L.Input.check(index: $0, on: true), .check(index: $0, on: false)] }
            case .text: inputs = [.text("abc"), .text("")]
            case .path: inputs = [.path("/x/y"), .path(nil)]
            case .hotkey: inputs = [.hotkey("ctrl+alt+KeyK")]
            }
            XCTAssertFalse(inputs.isEmpty, f.path)
            var committed = 0
            for input in inputs {
                guard case .success(let v) = L.commitValue(s, input) else { continue }
                committed += 1
                XCTAssertTrue(parseAccepts(f.kind, v), "\(f.path) committed \(v), which parse changes")
            }
            XCTAssertGreaterThan(committed, 0, "\(f.path) committed nothing, so the round-trip checked nothing")
        }
    }

    func test_hotkeys_read_in_mac_symbols() {
        XCTAssertEqual(L.formatHotkey("alt+super+Space", platform: .macos), "⌥⌘Space")
        XCTAssertEqual(L.formatHotkey("ctrl+alt+shift+super+KeyK", platform: .macos), "⌃⌥⇧⌘K")
        XCTAssertEqual(L.formatHotkey("super+Digit1", platform: .macos), "⌘1")
        XCTAssertEqual(L.formatHotkey("ctrl+ArrowUp", platform: .macos), "⌃↑")
        XCTAssertEqual(L.formatHotkey("alt+Comma", platform: .macos), "⌥,")
        XCTAssertEqual(L.formatHotkey("F5", platform: .macos), "F5")
        XCTAssertEqual(L.formatHotkey("super+KeyAB", platform: .macos), "⌘KeyAB")
        XCTAssertEqual(L.formatHotkey("", platform: .macos), "")
        XCTAssertEqual(L.formatHotkey("ctrl+alt+KeyK", platform: .windows), "Ctrl+Alt+K")
        XCTAssertEqual(L.formatHotkey("super+ArrowUp", platform: .linux), "Super+Up")
        XCTAssertEqual(L.formatHotkey("super+Space", platform: .windows), "Win+Space")
    }

    func test_recorder_and_placeholder_texts_match_the_web_page() {
        XCTAssertEqual(L.recording, "Type shortcut…")
        XCTAssertEqual(L.notAvailable, "(not available on this platform)")
        XCTAssertEqual(L.notFound, "(not found)")
    }

    func test_chrome_texts_match_the_web_page() {
        XCTAssertEqual(L.bannerText("line 3"), "settings.json has a syntax error; fix it or reset it (line 3)")
        let fields = SettingsFixture.fields
        XCTAssertNil(L.restartText([], fields: fields))
        XCTAssertEqual(L.restartText(["voice.provider", "voice.model"], fields: fields),
                       "Restart to apply: Voice service, Model")
        XCTAssertEqual(L.saveNotice("disk full"), "Couldn't save settings: disk full")
    }

    func test_errors_go_under_their_fields_and_name_others_under_the_committed_one() {
        let fields = SettingsFixture.fields
        let own = [SettingsFieldError(path: "agent.port", message: "Use a whole number from 1024 to 65535")]
        let placed = L.placeErrors("agent.port", own, fields: fields)
        XCTAssertEqual(placed.map(\.path), ["agent.port"])
        XCTAssertEqual(placed.map(\.message), ["Use a whole number from 1024 to 65535"])
        let other = [SettingsFieldError(path: "agent.terminal", message: "Not one of the choices")]
        let both = L.placeErrors("agent.enabled", other, fields: fields)
        XCTAssertEqual(both.map(\.path), ["agent.terminal", "agent.enabled"])
        XCTAssertEqual(both.map(\.message), ["Not one of the choices", "Terminal: Not one of the choices"])
    }

    func test_typed_text_commits_only_when_it_differs_from_what_the_field_was_given() {
        XCTAssertFalse(L.shouldCommit(typed: "8080", shown: "8080", hasDraft: false, hasError: false),
                       "an untouched field never re-commits")
        XCTAssertTrue(L.shouldCommit(typed: "8081", shown: "8080", hasDraft: false, hasError: false))
        XCTAssertTrue(L.shouldCommit(typed: " 8080 ", shown: "8080", hasDraft: false, hasError: false),
                      "the service normalises it, not this rule")
        XCTAssertTrue(L.shouldCommit(typed: "8080", shown: "8080", hasDraft: true, hasError: false), "a kept draft is retried")
        XCTAssertTrue(L.shouldCommit(typed: "8080", shown: "8080", hasDraft: false, hasError: true),
                      "a field showing an error is retried")
    }

    func test_newer_snapshots_win() {
        XCTAssertTrue(L.isNewer(nil, 1))
        XCTAssertTrue(L.isNewer(3, 4))
        XCTAssertFalse(L.isNewer(4, 4))
        XCTAssertFalse(L.isNewer(5, 4))
    }

    func test_extra_rows_match_the_web_page() {
        guard case .button(_, _, let title, let action)? = L.extras(.pixie).first, L.extras(.pixie).count == 1
        else { return XCTFail() }
        XCTAssertEqual(title, "Reset Position")
        XCTAssertEqual(action, .resetPosition)
        guard case .button(_, _, let t2, let a2)? = L.extras(.advanced).first, L.extras(.advanced).count == 1
        else { return XCTFail() }
        XCTAssertEqual(t2, "Open Settings File")
        XCTAssertEqual(a2, .openFile)
        XCTAssertEqual(L.extras(.voice), [
            .secret(id: .openAI, label: "OpenAI key",
                    help: "Type the key here to keep it in the Keychain (used first), or set OPENAI_API_KEY in your environment or a .env file. Keys are never shown here."),
            .secret(id: .gemini, label: "Gemini key",
                    help: "Type the key here to keep it in the Keychain (used first), or set GEMINI_API_KEY in your environment or a .env file. Keys are never shown here."),
        ])
        XCTAssertTrue(L.extras(.workers).isEmpty)
        guard case .pix(let help)? = L.extras(.agent).first, L.extras(.agent).count == 2
        else { return XCTFail("the agent section has the pix command row") }
        XCTAssertTrue(help.contains("pix claude"), help)
        // #179: then Restart Coordinator…, which asks first.
        guard case .button(let label, _, let t3, let a3)? = L.extras(.agent).last else { return XCTFail() }
        XCTAssertEqual(label, "Restart coordinator")
        XCTAssertEqual(t3, "Restart Coordinator…")
        XCTAssertEqual(a3, .restartCoordinator)
        let s = SettingsSecrets(openai: .found, gemini: .missing)
        XCTAssertEqual(L.secretText(s, .openAI), "Found")
        XCTAssertEqual(L.secretText(s, .gemini), "Missing")
        XCTAssertEqual(L.secretText(nil, .gemini), "Unknown")
    }
}
