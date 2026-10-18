import XCTest
@testable import DeskPixieCore

/// Ports of ai-mascot `src-tauri/src/tray.rs` tests (5) plus tree-shape tests for the menu
/// built from `app.state`.
///
/// Omitted Rust tests (the contract moves that formatting into the Go core, which sends
/// finished strings in AppState — P14 ports them in Go):
/// - `agent_job_rows_show_task_status_and_age`: job labels arrive preformatted (`jobs[].label`).
/// - `the_agent_status_line_prefers_the_runtime_over_the_integration_hint`: the agent line
///   arrives in `statusLines`.
final class TrayMenuTests: XCTestCase {
    // MARK: Rust ports

    func test_a_preset_size_is_checked_and_a_custom_one_gets_its_own_row() {
        let presets = TrayMenu.sizeItems(scale: 8)
        XCTAssertEqual(presets.count, TrayMenu.sizes.count)
        XCTAssertEqual(presets.filter { $0.checked == true }.map(\.id), ["size:8"])
        let custom = TrayMenu.sizeItems(scale: 5)
        XCTAssertEqual(custom.count, TrayMenu.sizes.count + 1)
        XCTAssertTrue(custom.prefix(TrayMenu.sizes.count).allSatisfy { $0.checked == false })
        XCTAssertEqual(custom.last, TrayMenu.Row(id: "size:5", title: "Custom (×5)", checked: true))
    }

    func test_recent_items_use_canvas_page_ids_and_titles() {
        let items = TrayMenu.recentItems([.init(slug: "q3", title: "Q3 revenue"), .init(slug: "db", title: "DB options")])
        XCTAssertEqual(items.map(\.id), ["canvas:page:q3", "canvas:page:db"])
        XCTAssertEqual(items.map(\.title), ["Q3 revenue", "DB options"])
    }

    func test_long_titles_are_cut_to_60_characters() {
        let title = String(repeating: "é", count: 80)
        let label = TrayMenu.recentItems([.init(slug: "long", title: title)])[0].title
        XCTAssertEqual(label.count, 60)
        XCTAssertTrue(label.hasSuffix("…"))
    }

    func test_preset_labels() {
        XCTAssertEqual(TrayMenu.sizeItems(scale: 8).map(\.title),
                       ["Small (×4)", "Medium (×6)", "Large (×8)", "Huge (×12)"])
    }

    // MARK: Tree

    private func state(_ json: String) throws -> TrayAppState {
        try JSONDecoder().decode(TrayAppState.self, from: Data(json.utf8))
    }

    private func titles(_ items: [TrayItem]) -> [String] {
        items.map {
            switch $0 {
            case .label(let t): return "label:\(t)"
            case .separator: return "--"
            case .action(let a): return a.id
            case .submenu(let title, _, _): return "menu:\(title)"
            }
        }
    }

    private func submenu(_ items: [TrayItem], _ title: String) -> (enabled: Bool, items: [TrayItem])? {
        for case .submenu(let t, let enabled, let children) in items where t == title { return (enabled, children) }
        return nil
    }

    func testFullTreeOrder() throws {
        let s = try state(#"""
        {"state":"thinking","packs":[{"id":"bit","name":"Bit"},{"id":"zed","name":"Zed"}],"pack":"bit","scale":6,
         "fade":true,"talking":false,"canvasRecent":[{"slug":"q3","title":"Q3"}],
         "agent":{"enabled":true,"jobs":[{"id":"job_1","label":"fix it — working, 4 s"}]},
         "statusLines":["Voice: connection lost","Coordinator: herdr not found"],"hotkey":"Ctrl+Alt+Space"}
        """#)
        let items = TrayMenu.build(s, localStatusLines: ["Hotkey unavailable: ⌃⌥Space"], micModeAvailable: true)
        XCTAssertEqual(titles(items), [
            "label:Hotkey unavailable: ⌃⌥Space", "--",
            "label:Voice: connection lost", "--",
            "label:Coordinator: herdr not found", "--",
            "menu:State", "talk", "menu:Size", "reset-position", "fade", "menu:Pixie", "reload", "menu:Canvas",
            "menu:Coordinator", "--", "mic-mode", "onboarding", "settings", "--", "quit",
        ])
        let st = try XCTUnwrap(submenu(items, "State"))
        XCTAssertEqual(titles(st.items), TrayMenu.stateNames.map { "state:\($0)" })
        XCTAssertTrue(st.items.contains(.action(.init(id: "state:thinking", title: "thinking", checked: true))))
        XCTAssertTrue(items.contains(.action(.init(id: "fade", title: "Proximity fade", checked: true))))
        XCTAssertTrue(items.contains(.action(.init(id: "talk", title: "Simulate talking", checked: false))))
        XCTAssertTrue(items.contains(.action(.init(id: "settings", title: "Settings…", shortcut: ","))))
        let pixie = try XCTUnwrap(submenu(items, "Pixie"))
        XCTAssertEqual(pixie.items, [.action(.init(id: "pack:bit", title: "Bit", checked: true)),
                                      .action(.init(id: "pack:zed", title: "Zed", checked: false))])
        let size = try XCTUnwrap(submenu(items, "Size"))
        XCTAssertTrue(size.items.contains(.action(.init(id: "size:6", title: "Medium (×6)", checked: true))))
        let canvas = try XCTUnwrap(submenu(items, "Canvas"))
        XCTAssertEqual(titles(canvas.items), ["canvas:open", "menu:Recent", "canvas:folder"])
        let recent = try XCTUnwrap(submenu(canvas.items, "Recent"))
        XCTAssertTrue(recent.enabled)
        XCTAssertEqual(titles(recent.items), ["canvas:page:q3"])
        let agent = try XCTUnwrap(submenu(items, "Coordinator"))
        XCTAssertEqual(titles(agent.items), ["agent-job:job_1", "menu:Cancel job", "agent:open", "agent:restart"])
        XCTAssertEqual(agent.items.first, .action(.init(id: "agent-job:job_1", title: "fix it — working, 4 s", enabled: false)))
        let cancel = try XCTUnwrap(submenu(agent.items, "Cancel job"))
        XCTAssertTrue(cancel.enabled)
        XCTAssertEqual(titles(cancel.items), ["agent:cancel:job_1"])
    }

    func testMissingDataHidesRows() throws {
        // What the temporary app.state returns before P14: no agent, no recent pages.
        let s = try state(#"{"state":"idle","packs":[],"pack":"","scale":8,"statusLines":[]}"#)
        let items = TrayMenu.build(s, localStatusLines: [], micModeAvailable: false)
        XCTAssertNil(submenu(items, "Coordinator"))
        XCTAssertNil(submenu(items, "Pixie"), "no packs, no Pixie submenu")
        XCTAssertFalse(titles(items).contains("mic-mode"))
        let recent = try XCTUnwrap(submenu(try XCTUnwrap(submenu(items, "Canvas")).items, "Recent"))
        XCTAssertFalse(recent.enabled)
        XCTAssertEqual(titles(items).first, "menu:State")
    }

    func testNoAppStateShowsOnlyAppItems() {
        let items = TrayMenu.build(nil, localStatusLines: ["Engine error: x"], micModeAvailable: true)
        XCTAssertEqual(titles(items), ["label:Engine error: x", "--", "mic-mode", "onboarding", "settings", "--", "quit"])
    }

    func testEmptyAgentHasDisabledCancel() throws {
        let s = try state(#"{"state":"idle","agent":{"enabled":true,"jobs":[]}}"#)
        let agent = try XCTUnwrap(submenu(TrayMenu.build(s, localStatusLines: [], micModeAvailable: true), "Coordinator"))
        XCTAssertEqual(try XCTUnwrap(submenu(agent.items, "Cancel job")).enabled, false)
    }

    func testDuplicateStatusLinesShownOnce() throws {
        let s = try state(#"{"state":"idle","statusLines":["Voice: model error"]}"#)
        let items = TrayMenu.build(s, localStatusLines: ["Voice: model error"], micModeAvailable: false)
        XCTAssertEqual(titles(items).filter { $0.hasPrefix("label:") }, ["label:Voice: model error"])
    }

    func testLocalIDs() {
        XCTAssertTrue(TrayMenu.isLocal("quit"))
        XCTAssertTrue(TrayMenu.isLocal("settings"))
        XCTAssertTrue(TrayMenu.isLocal("mic-mode"))
        XCTAssertFalse(TrayMenu.isLocal("state:idle"))
    }

    // MARK: Mic Mode (ports of crates/audio-macos/src/mic_modes.rs tests, 2)

    func test_the_picker_is_found_on_this_mac() {
        XCTAssertTrue(MicModes.available)
    }

    func test_it_is_a_class_method_not_an_instance_method() {
        XCTAssertTrue(MicModes.isInstanceMethod == false)
    }

    // MARK: Manual-mode fix

    private func fixRow(_ items: [TrayItem], _ title: String) -> TrayMenu.Row? {
        for case .action(let row) in items where row.title == title { return row }
        return nil
    }

    func test_a_mode_fix_with_an_id_is_an_action() throws {
        let s = try state(#"{"statusLines":["Coordinator is in Manual mode: its model is too old for auto mode"],"agentModeFix":{"id":"agent:fix-model","title":"Use a supported model"}}"#)
        let fix = try XCTUnwrap(fixRow(TrayMenu.build(s, localStatusLines: [], micModeAvailable: false), "Use a supported model"))
        XCTAssertTrue(fix.enabled)
        XCTAssertEqual(TrayMenu.route(fix.id), .core("agent:fix-model"))
    }

    func test_a_mode_fix_with_copy_copies_and_calls_nothing() throws {
        let s = try state(#"{"statusLines":["A worker is in Manual mode: Claude Code is too old for auto mode"],"agentModeFix":{"title":"Copy update command","copy":"claude update"}}"#)
        let items = TrayMenu.build(s, localStatusLines: [], micModeAvailable: false)
        let fix = try XCTUnwrap(fixRow(items, "Copy update command"))
        XCTAssertEqual(TrayMenu.route(fix.id), .copy("claude update"))
        XCTAssertTrue(TrayMenu.isLocal(fix.id))
        // sits right under the status line
        XCTAssertEqual(titles(items).prefix(3).map { $0 }, ["label:A worker is in Manual mode: Claude Code is too old for auto mode", fix.id, "--"])
    }
}
