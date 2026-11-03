import XCTest
@testable import DeskPixieCore

final class AgentRosterModelTests: XCTestCase {
    private func row(_ name: String, kind: String = "worker", status: String = "idle",
                     reachable: Bool = true, app: String? = nil) -> RosterRow {
        RosterRow(name: name, label: name, detail: nil, status: status, kind: kind, reachable: reachable, app: app)
    }

    private func view(agents: [RosterRow], yours: [RosterRow]) -> RosterView {
        RosterView(agents: agents, yours: yours,
                   agentCount: agents.filter { $0.kind != "coordinator" }.count,
                   yourCount: yours.filter(\.reachable).count)
    }

    func testCoordinatorAloneHidesTheChip() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("coordinator", kind: "coordinator")], yours: []))
        XCTAssertFalse(m.visible)
    }

    func testChipTextLeavesOutAZeroSide() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("coordinator", kind: "coordinator"), row("a"), row("b"), row("c")], yours: []))
        XCTAssertEqual(m.chipText, "⚙ 3")
        m.apply(view(agents: [row("coordinator", kind: "coordinator")], yours: [row("y", kind: "session")]))
        XCTAssertEqual(m.chipText, "⌨ 1")
        m.apply(view(agents: [row("coordinator", kind: "coordinator"), row("a")],
                     yours: [row("y", kind: "session"), row("z", kind: "session")]))
        XCTAssertEqual(m.chipText, "⚙ 1 · ⌨ 2")
    }

    func testSectionsCountWithoutTheCoordinatorAndCapRows() {
        var m = AgentRosterModel()
        let workers = (1...10).map { row("w\($0)") }
        m.apply(view(agents: [row("coordinator", kind: "coordinator")] + workers, yours: [row("y", kind: "session")]))
        let s = m.sections
        XCTAssertEqual(s.map(\.title), ["Pixie's agents (10)", "Your sessions (1)"])
        XCTAssertEqual(s[0].rows.first?.kind, "coordinator")
        XCTAssertEqual(s[0].rows.count, AgentRosterModel.maxRows)
        XCTAssertEqual(s[0].more, 11 - AgentRosterModel.maxRows)
        XCTAssertEqual(s[1].more, 0)
    }

    func testGoingEmptyClosesTheList() {
        var m = AgentRosterModel()
        m.apply(view(agents: [row("a")], yours: []))
        m.open = true
        m.apply(view(agents: [], yours: []))
        XCTAssertFalse(m.open)
    }

    func testClickable() {
        XCTAssertTrue(AgentRosterModel.clickable(row("a")))
        XCTAssertTrue(AgentRosterModel.clickable(row("y", kind: "session", app: "com.googlecode.iterm2")))
        // #148: the core finds your session's tab from its process, not its bundle id.
        XCTAssertTrue(AgentRosterModel.clickable(row("y", kind: "session")))
        XCTAssertFalse(AgentRosterModel.clickable(row("y", kind: "session", reachable: false, app: "com.apple.Terminal")))
    }

    func testDots() {
        XCTAssertEqual(["working", "blocked", "idle", "done", "unknown"].map(AgentRosterModel.dot), ["●", "◐", "○", "○", "·"])
    }

    // #234: the chip's busy dot follows the core's `busy`, and only while the chip shows.
    func testTheBusyDotFollowsTheCore() throws {
        let json = #"{"agents":[{"name":"coordinator","label":"Coordinator","status":"working","kind":"coordinator","reachable":true},{"name":"w","label":"w","status":"working","kind":"worker","reachable":true}],"yours":[],"agent_count":1,"your_count":0,"busy":true}"#
        var m = AgentRosterModel()
        m.apply(try JSONDecoder().decode(RosterView.self, from: Data(json.utf8)))
        XCTAssertTrue(m.busy)
        // The coordinator alone shows no chip, so no dot either.
        m.apply(RosterView(agents: [row("coordinator", kind: "coordinator", status: "working")], yours: [],
                           agentCount: 0, yourCount: 0, busy: true))
        XCTAssertFalse(m.busy)
        m.apply(RosterView(agents: [row("a")], yours: [], agentCount: 1, yourCount: 0, busy: false))
        XCTAssertFalse(m.busy)
    }

    // A roster without `busy` (an older core) decodes as not busy.
    func testARosterWithoutBusyIsNotBusy() throws {
        let json = #"{"agents":[],"yours":[],"agent_count":0,"your_count":0}"#
        XCTAssertFalse(try JSONDecoder().decode(RosterView.self, from: Data(json.utf8)).busy)
    }

    /// #261: the caption's buttons are the named sessions still open and reachable, in the
    /// order named; Pixie's own agents and closed sessions get none.
    func test_the_captions_buttons_are_the_named_sessions_you_can_reach() {
        let view = RosterView(agents: [row("w1")],
                              yours: [row("fruit-claude", kind: "session"), row("web-claude", kind: "session"),
                                      row("gone-claude", kind: "session", reachable: false)],
                              agentCount: 1, yourCount: 2)
        let got = AgentRosterModel.buttons(for: ["web-claude", "w1", "gone-claude", "fruit-claude", "unknown"], in: view)
        XCTAssertEqual(got.map(\.name), ["web-claude", "fruit-claude"])
        XCTAssertEqual(AgentRosterModel.buttons(for: [], in: view), [])
    }

    // MARK: The stop button (#309)

    func testTheStopButtonShowsOnlyWhereTheCoreOffersIt() throws {
        let json = #"""
        {"agents":[{"name":"coordinator","label":"Coordinator","status":"working","kind":"coordinator","reachable":true,"stoppable":true},
                   {"name":"docs-writer","label":"docs-writer","status":"idle","kind":"worker","reachable":true}],
         "yours":[{"name":"api","label":"api-server","detail":"terminal","status":"working","kind":"session","reachable":true,"stoppable":true}],
         "agent_count":1,"your_count":1,"busy":true}
        """#
        let v = try JSONDecoder().decode(RosterView.self, from: Data(json.utf8))
        XCTAssertEqual(v.agents.map(AgentRosterModel.showsStop), [true, false], "stoppable left out means no")
        XCTAssertTrue(AgentRosterModel.showsStop(v.yours[0]))
        XCTAssertEqual(AgentRosterModel.stopLabel(v.yours[0]), "Stop api-server")
        XCTAssertEqual(AgentRosterModel.stopLabel(v.agents[0]), "Stop Coordinator")
    }

    func testAStopsNoticeGoesWhenTheListCloses() {
        var m = AgentRosterModel()
        m.open = true
        m.stopFailed("Couldn't stop docs-writer: it isn't running anymore.")
        m.apply(view(agents: [row("coordinator", kind: "coordinator"), row("a")], yours: []))
        XCTAssertNotNil(m.notice, "a new roster keeps it while the list is open")
        m.open = false
        XCTAssertNil(m.notice)
    }

    func testAStopsNoticeIsSetOnlyWhileTheListIsOpen() {
        var m = AgentRosterModel()
        m.stopFailed("Couldn't stop docs-writer: it isn't running anymore.")
        XCTAssertTrue(m.beginStop("docs-writer"))
        m.endStop("docs-writer", .failure(DeskPixieEngineError.refused("The coordinator is off.")))
        XCTAssertNil(m.notice, "no stale line the next time it opens")
        m.open = true
        XCTAssertNil(m.notice)
    }

    func testARowsStopWaitsForItsCall() {
        var m = AgentRosterModel()
        XCTAssertTrue(m.beginStop("docs-writer"))
        XCTAssertFalse(m.beginStop("docs-writer"), "a second click while the first is on its way")
        XCTAssertTrue(m.beginStop("api"), "another row's is its own")
        XCTAssertEqual(m.stopping, ["docs-writer", "api"])
        m.endStop("docs-writer", .success(()))
        XCTAssertTrue(m.beginStop("docs-writer"))
    }

    func testAFailedStopShowsOnlyWhereItsClickCameFrom() throws {
        XCTAssertTrue(StopSurface.roster.shows(.roster))
        XCTAssertFalse(StopSurface.chat.shows(.roster), "a roster stop leaves the chat alone")
        XCTAssertFalse(StopSurface.roster.shows(.chat))
        XCTAssertTrue(StopSurface.chat.shows(nil), "an older core that doesn't say: both")
        let args = try JSONSerialization.jsonObject(with: JSONEncoder().encode(InterruptArgs(name: "a", surface: .chat))) as? [String: String]
        XCTAssertEqual(args, ["name": "a", "surface": "chat"])
    }

    func testAFailedStopCallReadsInTheCoresWordsOrTryAgain() {
        var m = AgentRosterModel()
        m.open = true
        _ = m.beginStop("a")
        m.endStop("a", .failure(DeskPixieEngineError.refused("The coordinator is off.")))
        XCTAssertEqual(m.notice, "The coordinator is off.")
        _ = m.beginStop("a")
        m.endStop("a", .failure(DeskPixieEngineError.callFailed("unknown method agent.interrupt")))
        XCTAssertEqual(m.notice, "Couldn't reach Pixie. Try again.", "never the core's internals")
        m.stopFailed("Couldn't stop a: it didn't answer in time.")
        XCTAssertEqual(m.notice, "Couldn't stop a: it didn't answer in time.")
        XCTAssertEqual(AgentRosterModel.stopFailure(DeskPixieEngineError.notInitialized), "Couldn't reach Pixie. Try again.")
    }

    // MARK: Turning Pixie off for a session (#431)

    private func session(_ name: String, off: Bool, reachable: Bool = true) -> RosterRow {
        RosterRow(name: name, label: name, detail: nil, status: "idle", kind: "session", reachable: reachable, app: nil, off: off)
    }

    func testAnOffSessionDecodesAndIsOnWhenLeftOut() throws {
        let json = #"{"agents":[],"yours":[{"name":"a","label":"a","status":"idle","kind":"session","reachable":true,"off":true},{"name":"b","label":"b","status":"idle","kind":"session","reachable":true}],"agent_count":0,"your_count":1}"#
        let v = try JSONDecoder().decode(RosterView.self, from: Data(json.utf8))
        XCTAssertEqual(v.yours.map(\.off), [true, false])
    }

    func testTheChipStaysWhileOnlyOffSessionsAreLeftSoYouCanTurnThemBackOn() {
        var m = AgentRosterModel()
        m.apply(RosterView(agents: [row("coordinator", kind: "coordinator")], yours: [session("a", off: true)], agentCount: 0, yourCount: 0))
        XCTAssertTrue(m.visible)
        XCTAssertEqual(m.offCount, 1)
        XCTAssertEqual(m.chipText, "⏾ 1")
        m.apply(RosterView(agents: [], yours: [session("a", off: true), session("b", off: false), session("c", off: true, reachable: false)],
                           agentCount: 0, yourCount: 1))
        XCTAssertEqual(m.offCount, 1, "an unreachable session isn't counted")
        XCTAssertEqual(m.chipText, "⌨ 1 · ⏾ 1")
    }

    func testOnlyYourReachableSessionsHaveASwitch() {
        XCTAssertTrue(AgentRosterModel.showsSwitch(session("a", off: false)))
        XCTAssertTrue(AgentRosterModel.showsSwitch(session("a", off: true)))
        XCTAssertFalse(AgentRosterModel.showsSwitch(session("a", off: false, reachable: false)))
        XCTAssertFalse(AgentRosterModel.showsSwitch(row("w")))
        XCTAssertFalse(AgentRosterModel.showsSwitch(row("coordinator", kind: "coordinator")))
    }

    func testTheSwitchSaysWhatAClickDoes() {
        XCTAssertEqual(AgentRosterModel.switchLabel(session("api", off: false)), "Turn Pixie off for api")
        XCTAssertEqual(AgentRosterModel.switchLabel(session("api", off: true)), "Turn Pixie back on for api")
    }

    func testAClickOnTheSwitchShowsAtOnceAndAFailureTurnsItBack() {
        var m = AgentRosterModel()
        m.open = true
        m.apply(RosterView(agents: [], yours: [session("a", off: false)], agentCount: 0, yourCount: 1))
        XCTAssertTrue(m.beginSwitch("a", off: true))
        XCTAssertFalse(m.beginSwitch("a", off: false), "one call at a time")
        XCTAssertEqual(m.isOff(m.view.yours[0]), true, "shown off before the core says so")
        m.endSwitch("a", .failure(DeskPixieEngineError.refused("No session a.")))
        XCTAssertEqual(m.isOff(m.view.yours[0]), false)
        XCTAssertEqual(m.notice, "No session a.")
        XCTAssertTrue(m.beginSwitch("a", off: true))
        m.endSwitch("a", .success(()))
        XCTAssertEqual(m.isOff(m.view.yours[0]), true, "kept until the roster catches up")
        m.apply(RosterView(agents: [], yours: [session("a", off: true)], agentCount: 0, yourCount: 0))
        XCTAssertEqual(m.isOff(m.view.yours[0]), true)
        m.apply(RosterView(agents: [], yours: [session("a", off: false)], agentCount: 0, yourCount: 1))
        XCTAssertEqual(m.isOff(m.view.yours[0]), false, "the core's word once it has caught up")
    }

    func testSetSessionOffArgsEncode() throws {
        let data = try JSONEncoder().encode(SetSessionOffArgs(name: "api-claude", off: true))
        XCTAssertEqual(try JSONSerialization.jsonObject(with: data) as? [String: AnyHashable], ["name": "api-claude", "off": true])
    }
}
