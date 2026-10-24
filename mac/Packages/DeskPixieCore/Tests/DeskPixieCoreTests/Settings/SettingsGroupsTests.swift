import XCTest
@testable import DeskPixieCore

/// #290: each page in groups with headings; settings under a switch that's off are greyed out.
@MainActor
final class SettingsGroupsTests: XCTestCase {
    private typealias L = SettingsLayout

    private func titles(_ groups: [L.Group]) -> [String] { groups.map(\.title) }
    private func ids(_ group: L.Group) -> [String] { group.rows.map(\.id) }

    func test_a_page_is_its_groups_in_order_with_the_extras_ending_theirs() {
        let model = SettingsModel(client: FakeSettingsClient())
        model.load()
        let general = model.groups(in: .general)
        XCTAssertEqual(titles(general), ["Talking to Pixie", "Look"])
        XCTAssertEqual(ids(general[1]), ["pack", "scale", "proximityFade.enabled", "proximityFade.radius", "extra:resetPosition"])
        let voice = model.groups(in: .voice)
        XCTAssertEqual(voice.first?.title, "Voice service")
        XCTAssertEqual(Array(ids(voice[0]).suffix(2)), ["extra:key.openai", "extra:key.gemini"])
        XCTAssertEqual(titles(model.groups(in: .advanced)).last, "Files")
    }

    func test_a_group_with_only_extras_comes_after_the_fields_groups() {
        let f = SettingsField(path: "a", section: .sessions, group: "Yours", label: "A", kind: .bool)
        let groups = L.groups(in: .sessions, fields: [f],
                              extras: [SettingsExtraPlace(id: "pix", section: .sessions, group: "pix"),
                                       SettingsExtraPlace(id: "exportLogs", section: .advanced, group: "Files"),
                                       SettingsExtraPlace(id: "nothing-drawn", section: .sessions, group: "Yours")])
        XCTAssertEqual(titles(groups), ["Yours", "pix"])
        XCTAssertEqual(ids(groups[0]), ["a"], "an extra this app doesn't draw is left out")
        XCTAssertEqual(ids(groups[1]), ["extra:pix"])
    }

    func test_a_field_under_a_switch_thats_off_is_greyed_out_and_says_which() {
        let workers = SettingsField(path: "agent.workers.enabled", section: .workers, group: "Workers", label: "Workers", kind: .bool)
        let max = SettingsField(path: "agent.workers.max", section: .workers, group: "Workers", label: "Most workers at once",
                                kind: .int(min: 1, max: 4, menu: true),
                                enabledWhen: [SettingsCondition(key: "agent.workers.enabled", in: ["true"])])
        let off: JSONValue = ["agent": ["workers": ["enabled": false]]]
        let on: JSONValue = ["agent": ["workers": ["enabled": true]]]
        XCTAssertEqual(L.disabledNote(max, fields: [workers, max], values: off), "Turn on “Workers” to change this.")
        XCTAssertNil(L.disabledNote(max, fields: [workers, max], values: on))
        XCTAssertNil(L.disabledNote(workers, fields: [workers, max], values: off))

        let client = FakeSettingsClient()
        client.fields = [workers, max]
        client.values = off
        let model = SettingsModel(client: client)
        model.load()
        XCTAssertFalse(model.canChange(max))
        XCTAssertTrue(model.canChange(workers))
        XCTAssertNotNil(model.disabledNote(max))
        XCTAssertTrue(model.commit("agent.workers.enabled", .switch(true)))
        XCTAssertTrue(model.canChange(max), "turning the switch on frees it at once")
        XCTAssertNil(model.disabledNote(max))
        // An unreadable settings.json greys everything out.
        client.fileError = "bad"
        model.refresh()
        XCTAssertFalse(model.canChange(workers))
    }

    func test_a_field_round_trips_with_its_unit() throws {
        let f = SettingsFixture.view("voice.idleTimeoutSecs")
        XCTAssertEqual(f.unit, "seconds")
        let back = try JSONDecoder().decode(SettingsField.self, from: JSONEncoder().encode(f))
        XCTAssertEqual(back, f)
    }

    func test_a_search_result_says_its_page_and_group() {
        XCTAssertEqual(L.place(SettingsHit(path: "voice.inputDevice", section: .voice, group: "Microphone and speaker", label: "Microphone")),
                       "Voice › Microphone and speaker")
        XCTAssertEqual(L.place(SettingsHit(path: "x", section: .coordinator, label: "X")), "Coordinator")
        let hit = try! JSONDecoder().decode(SettingsHit.self, from: Data(#"{"path":"x","section":"Sessions","label":"X"}"#.utf8))
        XCTAssertEqual(hit.group, "")
        XCTAssertEqual(hit.help, "")
    }
}
