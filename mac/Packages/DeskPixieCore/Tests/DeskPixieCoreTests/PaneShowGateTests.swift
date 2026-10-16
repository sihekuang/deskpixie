import XCTest
@testable import DeskPixieCore

final class PaneShowGateTests: XCTestCase {
    private let w = PaneRequest(job: "job_1", worker: "focus-fix")

    func testNothingIsDueWithoutARequest() {
        var g = PaneShowGate()
        XCTAssertNil(g.due(now: 0, sinceLastKey: 60))
    }

    func testARequestIsShownAtOnceWhenTheUserIsntTyping() {
        var g = PaneShowGate()
        g.request(w, now: 0)
        XCTAssertEqual(g.due(now: 0.1, sinceLastKey: 2), w)
        XCTAssertNil(g.due(now: 0.2, sinceLastKey: 5), "shown once")
    }

    func testItWaitsWhileTheUserTypes() {
        var g = PaneShowGate()
        g.request(w, now: 0)
        XCTAssertNil(g.due(now: 1, sinceLastKey: 1.9))
        XCTAssertNil(g.due(now: 5, sinceLastKey: 0.3))
        XCTAssertEqual(g.due(now: 7, sinceLastKey: 2.0), w)
    }

    func testANewerRequestReplacesTheWaitingOne() {
        var g = PaneShowGate()
        g.request(w, now: 0)
        let coordinator = PaneRequest(job: "", worker: "")
        g.request(coordinator, now: 1)
        XCTAssertEqual(g.due(now: 2, sinceLastKey: 3), coordinator)
    }

    func testARequestWaitingTooLongIsDropped() {
        var g = PaneShowGate()
        g.request(w, now: 0)
        XCTAssertNil(g.due(now: PaneShowGate.maxWait + 1, sinceLastKey: 10))
        XCTAssertNil(g.due(now: PaneShowGate.maxWait + 2, sinceLastKey: 10))
    }
}
