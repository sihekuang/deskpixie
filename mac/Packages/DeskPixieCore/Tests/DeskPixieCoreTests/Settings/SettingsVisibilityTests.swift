import XCTest
@testable import DeskPixieCore

/// #85: a field's `visibleWhen` (Go `FieldView.VisibleWhen`) hides it unless every condition
/// holds for the values shown; GPT-Live's fields show only for GPT-Live.
@MainActor
final class SettingsVisibilityTests: XCTestCase {
    typealias L = SettingsLayout

    /// The Voice fields as the Go core sends them (`settings.Views`), trimmed to the first four.
    static let voiceJSON = #"""
    [
     {"path":"voice.provider","section":"Voice","label":"Voice service","help":"h",
      "kind":{"type":"choice","options":[
        {"value":"openai-realtime","label":"OpenAI Realtime","platforms":["macos","windows","linux"]},
        {"value":"openai-live","label":"OpenAI GPT-Live","platforms":["macos","windows","linux"]}]},
      "apply":"restart","platforms":["macos","windows","linux"]},
     {"path":"voice.liveDelegation","section":"Voice","label":"GPT-Live delegation","help":"h",
      "kind":{"type":"choice","options":[
        {"value":"backend","label":"OpenAI backend model","platforms":["macos","windows","linux"]},
        {"value":"agent","label":"Straight to the coordinator (experimental)","platforms":["macos","windows","linux"]}]},
      "apply":"restart","platforms":["macos","windows","linux"],
      "visibleWhen":[{"key":"voice.provider","in":["openai-live"]}]},
     {"path":"voice.liveBackendModel","section":"Voice","label":"Backend model (GPT-Live)","help":"h",
      "kind":{"type":"choice","options":[]},"apply":"restart","platforms":["macos","windows","linux"],
      "visibleWhen":[{"key":"voice.provider","in":["openai-live"]},{"key":"voice.liveDelegation","in":["backend"]}]},
     {"path":"voice.audio","section":"Voice","label":"Audio","help":"h","kind":{"type":"choice","options":[]},
      "apply":"restart","platforms":["macos","windows","linux"]}
    ]
    """#

    private var fields: [SettingsField] {
        try! JSONDecoder().decode([SettingsField].self, from: Data(Self.voiceJSON.utf8))
    }

    private func values(provider: String, delegation: String = "backend") -> JSONValue {
        ["voice": ["provider": .string(provider), "liveDelegation": .string(delegation), "liveBackendModel": "gpt-6-luna"]]
    }

    private func shown(_ values: JSONValue) -> [String] {
        L.fields(in: .voice, fields, platform: .macos, values: values).map(\.path)
    }

    func test_visible_when_decodes_and_is_nil_when_absent() {
        let f = fields
        XCTAssertNil(f[0].visibleWhen)
        XCTAssertNil(f[3].visibleWhen)
        XCTAssertEqual(f[1].visibleWhen, [SettingsCondition(key: "voice.provider", in: ["openai-live"])])
        XCTAssertEqual(f[2].visibleWhen, [SettingsCondition(key: "voice.provider", in: ["openai-live"]),
                                          SettingsCondition(key: "voice.liveDelegation", in: ["backend"])])
    }

    func test_a_condition_holds_for_one_of_its_values() {
        let c = SettingsCondition(key: "voice.provider", in: ["openai-live", "gemini-live"])
        XCTAssertTrue(c.holds(values(provider: "openai-live")))
        XCTAssertTrue(c.holds(values(provider: "gemini-live")))
        XCTAssertFalse(c.holds(values(provider: "openai-realtime")))
        XCTAssertFalse(c.holds([:]), "A missing value is \"\"")
        XCTAssertTrue(SettingsCondition(key: "voice.chimes", in: ["true"]).holds(["voice": ["chimes": true]]))
        XCTAssertTrue(SettingsCondition(key: "scale", in: ["8"]).holds(["scale": 8]))
    }

    func test_every_condition_must_hold() {
        let backend = fields[2]
        XCTAssertTrue(backend.isVisible(values(provider: "openai-live", delegation: "backend")))
        XCTAssertFalse(backend.isVisible(values(provider: "openai-live", delegation: "agent")))
        XCTAssertFalse(backend.isVisible(values(provider: "openai-realtime", delegation: "backend")))
        XCTAssertTrue(fields[0].isVisible([:]), "No conditions: always shown")
    }

    func test_the_gpt_live_fields_show_only_for_gpt_live_under_the_voice_service() {
        XCTAssertEqual(shown(values(provider: "openai-realtime")), ["voice.provider", "voice.audio"])
        XCTAssertEqual(shown(values(provider: "openai-live")),
                       ["voice.provider", "voice.liveDelegation", "voice.liveBackendModel", "voice.audio"])
        XCTAssertEqual(shown(values(provider: "openai-live", delegation: "agent")),
                       ["voice.provider", "voice.liveDelegation", "voice.audio"])
        XCTAssertEqual(L.fields(in: .voice, fields, platform: .macos).count, 4, "Without values, nothing is hidden")
    }

    func test_picking_a_voice_service_shows_and_hides_at_once_and_keeps_hidden_values() {
        let client = FakeSettingsClient()
        client.fields = fields
        client.values = values(provider: "openai-realtime")
        let model = SettingsModel(client: client)
        model.load()
        XCTAssertEqual(model.fields(in: .voice).map(\.path), ["voice.provider", "voice.audio"])

        XCTAssertTrue(model.commit("voice.provider", .menu(1)))
        XCTAssertEqual(model.fields(in: .voice).map(\.path),
                       ["voice.provider", "voice.liveDelegation", "voice.liveBackendModel", "voice.audio"])

        XCTAssertTrue(model.commit("voice.liveDelegation", .menu(1)))
        XCTAssertEqual(model.fields(in: .voice).map(\.path), ["voice.provider", "voice.liveDelegation", "voice.audio"])

        XCTAssertTrue(model.commit("voice.provider", .menu(0)))
        XCTAssertEqual(model.fields(in: .voice).map(\.path), ["voice.provider", "voice.audio"])
        XCTAssertEqual(model.snapshot?.values.at("voice.liveDelegation"), "agent", "Hidden fields keep their values")
        XCTAssertEqual(model.snapshot?.values.at("voice.liveBackendModel"), "gpt-6-luna")
        XCTAssertEqual(client.sets.map(\.field), ["voice.provider", "voice.liveDelegation", "voice.provider"])
    }

    func test_a_field_round_trips_its_conditions() throws {
        let data = try JSONEncoder().encode(fields)
        XCTAssertEqual(try JSONDecoder().decode([SettingsField].self, from: data), fields)
        let plain = String(decoding: try JSONEncoder().encode(fields[0]), as: UTF8.self)
        XCTAssertFalse(plain.contains("visibleWhen"))
    }
}
