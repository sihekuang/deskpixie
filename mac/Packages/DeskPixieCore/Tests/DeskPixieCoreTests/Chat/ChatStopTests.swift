import XCTest
@testable import DeskPixieCore

/// #309: the stop button by a working responder's name in the chat window.
@MainActor
final class ChatStopTests: XCTestCase {
    private func said(_ id: String, _ at: Int64, kind: String = "session", name: String = "", label: String = "",
                      status: String = "") -> ChatEntry {
        ChatEntry(id: id, at: at, who: ChatWho(kind: kind, name: name, label: label, status: status), text: id)
    }

    private func row(_ name: String, kind: String, status: String, stoppable: Bool) -> RosterRow {
        RosterRow(name: name, label: name, detail: nil, status: status, kind: kind, reachable: true, app: nil,
                  stoppable: stoppable)
    }

    private func model(_ entries: [ChatEntry], agents: [RosterRow] = [], yours: [RosterRow] = []) async -> (ChatThreadModel, FakeChatClient) {
        let client = FakeChatClient(entries)
        let m = ChatThreadModel(client: client)
        await m.load()
        m.applyRoster(RosterView(agents: agents, yours: yours, agentCount: agents.count, yourCount: yours.count))
        return (m, client)
    }

    func testOnlyAWorkingResponderGetsAStopButton() async {
        let (m, _) = await model([said("a1", 1, name: "api", label: "api-server"), said("d1", 2, name: "docs", label: "docs")],
                                 agents: [row("docs", kind: "worker", status: "idle", stoppable: false)],
                                 yours: [row("api", kind: "session", status: "working", stoppable: true)])
        XCTAssertEqual(m.stopTarget(for: m.entries[0]), ChatStopTarget(name: "api", label: "Stop api-server"))
        XCTAssertNil(m.stopTarget(for: m.entries[1]), "idle: none")
    }

    func testOnlyTheResponderNewestEntryGetsIt() async {
        let entries = [said("a1", 1, name: "api", label: "api-server"), said("c1", 2, kind: "coordinator"),
                       said("a2", 3, name: "api", label: "api-server"), said("p1", 4, kind: "pixie")]
        let (m, _) = await model(entries, agents: [row("coordinator", kind: "coordinator", status: "working", stoppable: true)],
                                 yours: [row("api", kind: "session", status: "working", stoppable: true)])
        XCTAssertEqual(m.entries.map { m.stopTarget(for: $0)?.name }, [nil, "coordinator", "api", nil])
        XCTAssertEqual(m.stopTarget(for: m.entries[1])?.label, "Stop Coordinator")
    }

    func testPixieYouAndAnEntryWithNoAddressHaveNone() async {
        let (m, _) = await model([said("p1", 1, kind: "pixie"), said("u1", 2, kind: "you"), said("s1", 3, label: "somebody")],
                                 yours: [row("", kind: "session", status: "working", stoppable: true)])
        XCTAssertTrue(m.entries.allSatisfy { m.stopTarget(for: $0) == nil })
    }

    func testTheButtonGoesWhenTheRosterSaysItStopped() async {
        let (m, _) = await model([said("a1", 1, name: "api", label: "api-server", status: "working")],
                                 yours: [row("api", kind: "session", status: "working", stoppable: true)])
        XCTAssertNotNil(m.stopTarget(for: m.entries[0]))
        XCTAssertEqual(ChatThreadModel.header(m.liveWho(m.entries[0])), "api-server · Pixie's agent · working")
        m.applyRoster(RosterView(agents: [], yours: [row("api", kind: "session", status: "idle", stoppable: false)],
                                 agentCount: 0, yourCount: 1))
        XCTAssertNil(m.stopTarget(for: m.entries[0]))
        XCTAssertEqual(m.liveWho(m.entries[0]).status, "idle", "the newest header reads the status now")
    }

    func testOlderEntriesKeepTheStatusTheyWereWrittenWith() async {
        let (m, _) = await model([said("a1", 1, name: "api", status: "working"), said("a2", 2, name: "api", status: "working")],
                                 yours: [row("api", kind: "session", status: "idle", stoppable: false)])
        XCTAssertEqual(m.liveWho(m.entries[0]).status, "working")
        XCTAssertEqual(m.liveWho(m.entries[1]).status, "idle")
    }

    func testStopCallsTheCoreWithTheRosterName() async {
        let (m, client) = await model([said("a1", 1, name: "api", label: "api-server")],
                                      yours: [row("api", kind: "session", status: "working", stoppable: true)])
        await m.stop(ChatStopTarget(name: "api", label: "Stop api-server"))
        XCTAssertEqual(client.interrupted, ["api"])
        XCTAssertNil(m.error)
        XCTAssertNotNil(m.stopTarget(for: m.entries[0]), "never faked: the roster takes it away")
    }

    func testARefusedStopSaysWhyInTheCoresWords() async {
        let (m, client) = await model([])
        client.refuseNextInterrupt("Couldn't stop api: it isn't running anymore.")
        await m.stop(ChatStopTarget(name: "api", label: "Stop api"))
        XCTAssertEqual(m.error, "Couldn't stop api: it isn't running anymore.")
        client.setFailing(true)
        await m.stop(ChatStopTarget(name: "api", label: "Stop api"))
        XCTAssertEqual(m.error, "Couldn't reach Pixie. Try again.")
    }

    func testAStopGoesOnceWhileItsCallRuns() async {
        let (m, client) = await model([said("a1", 1, name: "api", label: "api-server")],
                                      yours: [row("api", kind: "session", status: "working", stoppable: true)])
        client.holdInterrupt()
        let target = ChatStopTarget(name: "api", label: "Stop api-server")
        let first = Task { await m.stop(target) }
        while !m.stopping.contains("api") { await Task.yield() }
        XCTAssertEqual(m.stopTarget(for: m.entries[0])?.stopping, true, "the button waits")
        await m.stop(target) // a second click
        client.releaseInterrupt()
        await first.value
        XCTAssertEqual(client.interrupted, ["api"], "one call: no second Esc")
        XCTAssertEqual(m.stopTarget(for: m.entries[0])?.stopping, false)
    }

    func testAStopThatWorksClearsOnlyTheErrorAStopSet() async {
        let (m, client) = await model([])
        client.setFailing(true)
        await m.decide(token: "t", allow: true)
        XCTAssertEqual(m.error, "Couldn't reach Pixie. Try again.")
        client.setFailing(false)
        await m.stop(ChatStopTarget(name: "api", label: "Stop api"))
        XCTAssertEqual(m.error, "Couldn't reach Pixie. Try again.", "another call's error stays")
        client.refuseNextInterrupt("Couldn't stop it.")
        await m.stop(ChatStopTarget(name: "api", label: "Stop api"))
        XCTAssertEqual(m.error, "Couldn't stop it.")
        await m.stop(ChatStopTarget(name: "api", label: "Stop api"))
        XCTAssertNil(m.error, "its own goes")
    }

    func testAFailedStopEventShowsItsWords() async {
        let (m, _) = await model([])
        m.stopFailed("Couldn't stop alpha: it isn't running anymore.")
        XCTAssertEqual(m.error, "Couldn't stop alpha: it isn't running anymore.")
    }
}
