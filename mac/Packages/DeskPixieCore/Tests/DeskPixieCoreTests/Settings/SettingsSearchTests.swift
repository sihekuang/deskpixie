import XCTest
@testable import DeskPixieCore

/// The Settings window's search (#287): the model asks the core, the sidebar lists the hits,
/// and picking one shows its page with the row pointed out.
@MainActor
final class SettingsSearchTests: XCTestCase {
    private var client: FakeSettingsClient!
    private var model: SettingsModel!
    private let mic = SettingsHit(path: "voice.inputDevice", section: .voice, label: "Microphone", help: "The mic.")

    override func setUp() async throws {
        client = FakeSettingsClient()
        model = SettingsModel(client: client)
        model.load()
    }

    func test_typing_asks_the_core_and_lists_what_it_found() {
        client.searchResult = [mic]
        model.query = "mic"
        XCTAssertTrue(model.searching)
        XCTAssertEqual(client.queries, ["mic"])
        XCTAssertEqual(model.hits, [mic])
    }

    func test_a_blank_query_lists_the_pages_again_without_asking() {
        client.searchResult = [mic]
        model.query = "mic"
        model.query = "   "
        XCTAssertFalse(model.searching)
        XCTAssertEqual(model.hits, [])
        XCTAssertEqual(client.queries, ["mic"])
    }

    func test_a_failed_search_shows_no_results() {
        client.searchThrows = NSError(domain: "fake", code: 1)
        model.query = "mic"
        XCTAssertTrue(model.searching)
        XCTAssertEqual(model.hits, [])
    }

    func test_a_newer_snapshot_searches_again_while_searching() {
        model.query = "mic"
        model.refresh()
        XCTAssertEqual(client.queries, ["mic", "mic"])
        model.query = ""
        model.refresh()
        XCTAssertEqual(client.queries, ["mic", "mic"])
    }

    func test_a_page_points_out_a_reveal_once_even_when_the_window_is_reopened() {
        XCTAssertNil(model.takeReveal(for: .voice))
        model.reveal(mic)
        XCTAssertNil(model.takeReveal(for: .pixie))
        XCTAssertEqual(model.takeReveal(for: .voice), mic)
        // A new page view (the window reopened) asks again: already pointed out.
        XCTAssertNil(model.takeReveal(for: .voice))
        model.reveal(mic)
        XCTAssertEqual(model.takeReveal(for: .voice), mic)
    }

    func test_a_reveal_made_before_the_window_opens_picks_its_page_until_pointed_out() {
        XCTAssertNil(model.pendingRevealSection)
        model.reveal(mic)
        XCTAssertEqual(model.pendingRevealSection, .voice)
        _ = model.takeReveal(for: .voice)
        XCTAssertNil(model.pendingRevealSection)
    }

    func test_revealing_a_hit_points_it_out_each_time_and_stops_recording() {
        model.recordingPath = "hotkey"
        model.reveal(mic)
        XCTAssertEqual(model.revealed, mic)
        XCTAssertEqual(model.revealCount, 1)
        XCTAssertNil(model.recordingPath)
        model.reveal(mic)
        XCTAssertEqual(model.revealCount, 2)
    }

    func test_the_hit_decodes_from_the_cores_json() throws {
        let json = #"[{"path":"extra:key.gemini","section":"Voice","label":"Gemini key","help":"h"}]"#
        let hits = try JSONDecoder().decode([SettingsHit].self, from: Data(json.utf8))
        XCTAssertEqual(hits, [SettingsHit(path: "extra:key.gemini", section: .voice, label: "Gemini key", help: "h")])
    }

    func test_every_extra_row_has_the_id_the_core_searches_by() {
        // Go settings.Extras, in page order.
        let ids = SettingsLayout.tabs.flatMap { SettingsLayout.extras($0.section) }.map(\.id)
        XCTAssertEqual(Set(ids), ["resetPosition", "key.openai", "key.gemini", "pix", "restartCoordinator",
                                  "openFile", "exportLogs"])
        XCTAssertEqual(ids.count, Set(ids).count)
        XCTAssertEqual(SettingsLayout.extras(.voice).first?.path, "extra:key.openai")
    }

    func test_the_extra_rows_read_as_the_core_searches_them() {
        // Word for word Go's settings.Extras: the search matches on and shows these.
        let all = SettingsLayout.tabs.flatMap { SettingsLayout.extras($0.section) }
        let rows = Dictionary(uniqueKeysWithValues: all.map { ($0.id, [$0.label, $0.help]) })
        XCTAssertEqual(rows, [
            "resetPosition": ["Reset position", "Move Pixie back to the bottom-right corner of the main screen."],
            "key.openai": ["OpenAI key", "Type the key here to keep it in the Keychain (used first), or set OPENAI_API_KEY in your environment or a .env file. Keys are never shown here."],
            "key.gemini": ["Gemini key", "Type the key here to keep it in the Keychain (used first), or set GEMINI_API_KEY in your environment or a .env file. Keys are never shown here."],
            "pix": ["pix command", "Start Claude Code or Codex with `pix claude` or `pix codex` in any terminal (WebStorm, iTerm, …) and Pixie can see, message and interrupt that session. Quitting DeskPixie never ends it."],
            "restartCoordinator": ["Restart coordinator", "Stops the coordinator and starts it again on your next request, with its conversation. Asks first."],
            "openFile": ["Settings file", "settings.json holds every setting on this page."],
            "exportLogs": ["Logs", "Saves the app's log, pix's log, the voice log and metrics in one zip to share. It can include what you said to Pixie."],
        ])
        // Go's Extra.Title: the buttons' titles.
        let titles = all.compactMap { e -> String? in
            if case .button(_, _, let title, _) = e { return "\(e.id)=\(title)" }
            return nil
        }
        XCTAssertEqual(Set(titles), ["resetPosition=Reset Position", "openFile=Open Settings File",
                                     "exportLogs=Export Logs…", "restartCoordinator=Restart Coordinator…"])
    }

    func test_a_hit_names_its_page_as_the_sidebar_does() {
        XCTAssertEqual(SettingsLayout.tabTitle(.agent), "Coordinator")
        XCTAssertEqual(SettingsLayout.tabIndex(.voice), 1)
        XCTAssertEqual(SettingsLayout.noResultsText(" zebra "), "No settings match “zebra”.")
    }
}
