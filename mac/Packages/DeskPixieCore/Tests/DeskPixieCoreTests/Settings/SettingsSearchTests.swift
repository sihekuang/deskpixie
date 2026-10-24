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

    func test_a_hit_names_its_page_as_the_sidebar_does() {
        XCTAssertEqual(SettingsLayout.tabTitle(.agent), "Coordinator")
        XCTAssertEqual(SettingsLayout.tabIndex(.voice), 1)
        XCTAssertEqual(SettingsLayout.noResultsText(" zebra "), "No settings match “zebra”.")
    }
}
