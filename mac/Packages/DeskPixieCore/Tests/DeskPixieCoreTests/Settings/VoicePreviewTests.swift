import XCTest
@testable import DeskPixieCore

/// The voice list with previews (#480): the kind and option detail decode, the list filters,
/// and each row follows the core's answer and its `voice.preview` events.
@MainActor
final class VoicePreviewTests: XCTestCase {
    /// Answers like the core: refuses with its reason, or starts in the state it is given.
    final class FakePreviewClient: VoicePreviewClient {
        var calls: [String] = []
        var stops = 0
        var answer = "loading"
        var refusal: String?

        func preview(_ voice: String) throws -> String {
            calls.append(voice)
            if let refusal { throw DeskPixieEngineError.refused(refusal) }
            return answer
        }

        func stop() throws { stops += 1 }
    }

    typealias Item = SettingsLayout.MenuItem

    private let items = [
        Item(label: "Zephyr", value: "Zephyr", enabled: true, detail: "Bright"),
        Item(label: "Puck", value: "Puck", enabled: true, detail: "Upbeat"),
        Item(label: "Kore", value: "Kore", enabled: true, detail: "Firm"),
        Item(label: "Marin", value: "marin", enabled: true),
    ]

    func test_a_preview_choice_and_its_details_decode_and_round_trip() throws {
        let json = #"{"type":"choice","preview":true,"options":[{"value":"Kore","label":"Kore","detail":"Firm"},{"value":"Puck","label":"Puck"}]}"#
        let kind = try JSONDecoder().decode(SettingsKind.self, from: Data(json.utf8))
        XCTAssertEqual(kind, .previewChoice([SettingsOption(value: "Kore", label: "Kore", detail: "Firm"),
                                             SettingsOption(value: "Puck", label: "Puck")]))
        XCTAssertEqual(try JSONDecoder().decode(SettingsKind.self, from: JSONEncoder().encode(kind)), kind)
        let plain = try JSONDecoder().decode(SettingsKind.self, from: Data(#"{"type":"choice","options":[]}"#.utf8))
        XCTAssertEqual(plain, .choice([]), "no preview: a pop-up as before")
        XCTAssertEqual(SettingsLayout.controlKind(kind), .voices)
    }

    func test_the_voice_list_keeps_the_detail_and_commits_like_a_menu() {
        let field = SettingsField(path: "voice.voice", section: .voice, group: "Voice service", label: "Voice", help: "",
                                  kind: .previewChoice([SettingsOption(value: "Kore", label: "Kore", detail: "Firm"),
                                                        SettingsOption(value: "Puck", label: "Puck", detail: "Upbeat")]),
                                  apply: "restart")
        let spec = SettingsLayout.controlSpec(field, values: ["voice": ["voice": "Puck"]], platform: .macos)
        guard case .voices(let rows, let selected) = spec.control else { return XCTFail("\(spec.control)") }
        XCTAssertEqual(rows.map(\.detail), ["Firm", "Upbeat"])
        XCTAssertEqual(selected, 1)
        XCTAssertEqual(SettingsLayout.commitValue(spec, .menu(0)), .success("Kore"))
    }

    func test_the_search_matches_names_and_descriptions_in_any_case() {
        XCTAssertEqual(VoicePreviewModel.filter(items, ""), [0, 1, 2, 3])
        XCTAssertEqual(VoicePreviewModel.filter(items, "pu"), [1])
        XCTAssertEqual(VoicePreviewModel.filter(items, "FIRM"), [2])
        XCTAssertEqual(VoicePreviewModel.filter(items, "kore firm"), [2])
        XCTAssertEqual(VoicePreviewModel.filter(items, "kore bright"), [])
        XCTAssertEqual(VoicePreviewModel.filter(items, "  marin "), [3])
    }

    func test_play_follows_the_core_until_done() {
        let client = FakePreviewClient()
        let model = VoicePreviewModel(client: client)
        model.toggle("Kore")
        XCTAssertEqual(client.calls, ["Kore"])
        XCTAssertEqual(model.state("Kore"), .loading)
        model.apply(voice: "Kore", state: "playing", error: nil)
        XCTAssertEqual(model.state("Kore"), .playing)
        model.apply(voice: "Kore", state: "done", error: nil)
        XCTAssertEqual(model.state("Kore"), .idle)

        client.answer = "playing" // kept: plays at once
        model.toggle("Puck")
        XCTAssertEqual(model.state("Puck"), .playing)
    }

    func test_the_button_stops_a_preview_and_another_voice_takes_over() {
        let client = FakePreviewClient()
        let model = VoicePreviewModel(client: client)
        model.toggle("Kore")
        model.toggle("Kore")
        XCTAssertEqual(client.stops, 1, "■ on a loading voice stops it")
        XCTAssertEqual(model.state("Kore"), .idle)

        model.toggle("Kore")
        model.toggle("Puck")
        XCTAssertEqual(model.state("Kore"), .idle, "one voice at a time")
        XCTAssertEqual(model.state("Puck"), .loading)
    }

    func test_a_refusal_or_a_failure_shows_under_its_row() {
        let client = FakePreviewClient()
        client.refusal = "Add the voice service's API key to hear previews."
        let model = VoicePreviewModel(client: client)
        model.toggle("Kore")
        XCTAssertEqual(model.state("Kore"), .failed("Add the voice service's API key to hear previews."))

        client.refusal = nil
        model.toggle("Kore")
        XCTAssertEqual(model.state("Kore"), .loading, "▶ again tries again")
        model.apply(voice: "Kore", state: "error", error: "Couldn't make the preview: no connection")
        XCTAssertEqual(model.state("Kore"), .failed("Couldn't make the preview: no connection"))
        model.stop()
        XCTAssertEqual(model.state("Kore"), .failed("Couldn't make the preview: no connection"), "Stop leaves the reason")
    }

    func test_the_preview_event_decodes() {
        XCTAssertEqual(EngineEvent.decode(Data(#"{"kind":"voice.preview","voice":"Kore","state":"playing"}"#.utf8)),
                       .voicePreview(voice: "Kore", state: "playing", error: nil))
        XCTAssertEqual(EngineEvent.decode(Data(#"{"kind":"voice.preview","voice":"Kore","state":"error","error":"x"}"#.utf8)),
                       .voicePreview(voice: "Kore", state: "error", error: "x"))
        XCTAssertNil(EngineEvent.decode(Data(#"{"kind":"voice.preview","state":"done"}"#.utf8)))
    }
}
