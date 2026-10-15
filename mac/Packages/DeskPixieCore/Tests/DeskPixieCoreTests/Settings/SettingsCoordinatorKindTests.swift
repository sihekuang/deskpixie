import XCTest
@testable import DeskPixieCore

/// #87: Settings ▸ Coordinator's "Coordinator agent" picker (Claude Code or Codex); the model
/// list shown follows it (Go `visibleWhen`), and the hidden one keeps its value.
@MainActor
final class SettingsCoordinatorKindTests: XCTestCase {
    typealias L = SettingsLayout

    /// The fields as the Go core sends them (`settings.Views`), options trimmed.
    static let agentJSON = #"""
    [
     {"path":"agent.coordinatorKind","section":"Agent","label":"Coordinator agent","help":"h",
      "kind":{"type":"choice","options":[
        {"value":"claude","label":"Claude Code","platforms":["macos","windows","linux"]},
        {"value":"codex","label":"Codex","platforms":["macos","windows","linux"]}]},
      "apply":"restart","platforms":["macos","windows","linux"]},
     {"path":"agent.model","section":"Agent","label":"Coordinator model","help":"h",
      "kind":{"type":"choice","options":[
        {"value":"","label":"Default (Claude Code decides)","platforms":["macos","windows","linux"]},
        {"value":"opus","label":"Opus (latest)","platforms":["macos","windows","linux"]}]},
      "apply":"restart","platforms":["macos","windows","linux"],
      "visibleWhen":[{"key":"agent.coordinatorKind","in":["claude"]}]},
     {"path":"agent.codexModel","section":"Agent","label":"Coordinator model","help":"h",
      "kind":{"type":"choice","options":[
        {"value":"","label":"Default (Codex decides)","platforms":["macos","windows","linux"]},
        {"value":"gpt-6-sol","label":"GPT-6 Sol","platforms":["macos","windows","linux"]}]},
      "apply":"restart","platforms":["macos","windows","linux"],
      "visibleWhen":[{"key":"agent.coordinatorKind","in":["codex"]}]}
    ]
    """#

    private var fields: [SettingsField] {
        try! JSONDecoder().decode([SettingsField].self, from: Data(Self.agentJSON.utf8))
    }

    func test_the_model_list_follows_the_coordinator_agent() {
        let claude: JSONValue = ["agent": ["coordinatorKind": "claude", "model": "opus", "codexModel": nil]]
        let codex: JSONValue = ["agent": ["coordinatorKind": "codex", "model": "opus", "codexModel": "gpt-6-sol"]]
        XCTAssertEqual(L.fields(in: .agent, fields, platform: .macos, values: claude).map(\.path),
                       ["agent.coordinatorKind", "agent.model"])
        XCTAssertEqual(L.fields(in: .agent, fields, platform: .macos, values: codex).map(\.path),
                       ["agent.coordinatorKind", "agent.codexModel"])
    }

    func test_picking_codex_swaps_the_model_list_and_keeps_claudes_model() {
        let client = FakeSettingsClient()
        client.fields = fields
        client.values = ["agent": ["coordinatorKind": "claude", "model": "opus", "codexModel": nil]]
        let model = SettingsModel(client: client)
        model.load()
        XCTAssertEqual(model.fields(in: .agent).map(\.path), ["agent.coordinatorKind", "agent.model"])
        XCTAssertTrue(model.commit("agent.coordinatorKind", .menu(1)))
        XCTAssertEqual(model.fields(in: .agent).map(\.path), ["agent.coordinatorKind", "agent.codexModel"])
        XCTAssertEqual(model.snapshot?.values.at("agent.model"), "opus", "Claude workers still use it")
        XCTAssertEqual(client.sets.map(\.field), ["agent.coordinatorKind"])
    }
}
