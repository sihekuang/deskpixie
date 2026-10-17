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
}
