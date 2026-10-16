import XCTest
@testable import DeskPixieCore

/// Spec §4 lifecycle of the updates bubble beside Pixie: hidden / popped / badge / expanded, the
/// 1 s "seen" rule, away → badge only → one pop on return. Times are seconds on one clock.
final class UpdatesBubbleModelTests: XCTestCase {
    private func line(_ id: UInt64) -> UpdateLine {
        UpdateLine(id: id, text: "line \(id)", kind: "reply", at: Int64(id), job: nil, worker: nil)
    }

    /// #105: a session's menu offers its options instead of Allow and Deny.
    /// #105: a session's dialog carries its command, shown small under the line.
    func testADialogLineCarriesItsCommand() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":8,"text":"api: Run the tests. Do you want to proceed? Choose in the bubble.","kind":"question","at":8,"token":"owl","options":["Yes","No"],"note":"npm test"}"#.utf8))
        XCTAssertEqual(line.note, "npm test")
        XCTAssertTrue(line.offersOptions)
    }

    func testASessionMenuOffersItsOptions() throws {
        let menu = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":6,"text":"deskpixie is asking: Push main?","kind":"question","at":6,"token":"hawk","options":["Push","Don't push"]}"#.utf8))
        XCTAssertEqual(menu.options, ["Push", "Don't push"])
        XCTAssertTrue(menu.offersOptions)
        XCTAssertFalse(menu.offersDecision)
        let permission = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":7,"text":"Run npm test in api? Say confirm owl.","kind":"question","at":7,"token":"owl"}"#.utf8))
        XCTAssertTrue(permission.offersDecision)
        XCTAssertFalse(permission.offersOptions)
    }

    func testASessionLineCarriesItsApp() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":4,"text":"api is done: tests pass.","kind":"session","at":4,"app":"com.jetbrains.WebStorm"}"#.utf8))
        XCTAssertEqual(line.app, "com.jetbrains.WebStorm")
        XCTAssertFalse(line.offersDecision)
        let reply = try JSONDecoder().decode(UpdateLine.self, from: Data(#"{"id":5,"text":"Done.","kind":"reply","at":5}"#.utf8))
        XCTAssertNil(reply.app)
    }

    func testAPendingQuestionOffersAllowAndDeny() throws {
        let decode = { (json: String) throws -> UpdateLine in
            try JSONDecoder().decode(UpdateLine.self, from: Data(json.utf8))
        }
        let line = try decode(#"{"id":1,"text":"Run git push in focus? Say confirm hawk.","kind":"question","at":1,"token":"hawk"}"#)
        XCTAssertEqual(line.token, "hawk")
        XCTAssertTrue(line.offersDecision)
        let info = try decode(#"{"id":2,"text":"Done.","kind":"reply","at":2}"#)
        XCTAssertFalse(info.offersDecision)
        let jobQuestion = try decode(#"{"id":3,"text":"Which one?","kind":"question","at":3}"#)
        XCTAssertFalse(jobQuestion.offersDecision)
    }

    private func question(_ id: UInt64, _ token: String) -> UpdateLine {
        UpdateLine(id: id, text: "Run it? Say confirm \(token).", kind: "question", at: Int64(id), job: nil,
                   worker: nil, token: token)
    }

    /// #97: a click landing as the buttons appear never approves: Allow and Deny ignore taps for
    /// 0.75 s after that question's buttons appear.
    func testAllowAndDenyIgnoreTapsJustAfterTheButtonsAppear() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)       // pops: buttons appear at 10
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8))
    }

    /// Review of #105: a session menu's option buttons are guarded the same way, then count.
    func testMenuOptionsAcceptTapsOnceShownLongEnough() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let menu = UpdateLine(id: 1, text: "Push main?", kind: "question", at: 1, job: nil, worker: nil,
                              token: "hawk", options: ["Push", "Don't push"])
        m.apply([menu], now: 10, pop: .allowed)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8))
    }

    func testTheGuardIsPerQuestion() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        m.apply([question(1, "hawk"), question(2, "lynx")], now: 11, pop: .allowed)
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 11.2))
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 11.2))
        XCTAssertTrue(m.acceptsDecision(token: "lynx", now: 11.8))
    }

    func testButtonsThatReappearAreGuardedAgain() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        _ = m.tick(now: 17)                                         // collapsed to the badge
        XCTAssertEqual(m.phase, .badge)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 17.5)) // no buttons on a badge
        m.pointerEntered(now: 20)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 20.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 20.8))
    }

    func testATapOnAQuestionNotShownIsIgnored() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        XCTAssertFalse(m.acceptsDecision(token: "owl", now: 12))
    }

    func testFirstListAfterLaunchIsBadgeOnly() {
        var m = UpdatesBubbleModel()
        m.apply([line(1)], now: 0, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)
    }

    func testFirstEmptyListAfterLaunchIsHidden() {
        var m = UpdatesBubbleModel()
        XCTAssertEqual(m.phase, .hidden)
        m.apply([], now: 0, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testNewLinePopsThenCollapsesToBadge() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 10, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 16))
        XCTAssertEqual(m.tick(now: 15.9), []); XCTAssertEqual(m.phase, .popped(until: 16))
        _ = m.tick(now: 16); XCTAssertEqual(m.phase, .badge)
    }

    func testAnotherLineWhilePoppedRestartsTheTimer() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 10, pop: .allowed)
        m.apply([line(1), line(2)], now: 14, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 20))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
    }

    func testSameListAgainDoesNotPop() {
        var m = UpdatesBubbleModel(); m.apply([line(1)], now: 0, pop: .allowed)
        m.apply([line(1)], now: 5, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)
    }

    func testHoverExpandsAndOneSecondMarksSeenOnce() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        XCTAssertEqual(m.phase, .expanded(since: 2))
        XCTAssertEqual(m.tick(now: 2.9), [])
        XCTAssertEqual(Set(m.tick(now: 3)), [1, 2])
        XCTAssertEqual(m.tick(now: 4), [])
    }

    func testSeenMarksOnlyTheShownLines() {
        var m = UpdatesBubbleModel(popSecs: 6, maxLines: 2)
        m.apply((1...4).map(line), now: 0, pop: .allowed)
        m.pointerEntered(now: 1)
        XCTAssertEqual(Set(m.tick(now: 2)), [4, 3], "the +N more lines were not viewed")
    }

    func testPointerOverAnEmptyHiddenBubbleDoesNothing() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.pointerEntered(now: 1)
        XCTAssertEqual(m.phase, .hidden)
        XCTAssertEqual(m.tick(now: 3), [])
    }

    func testExpandedListStaysUntilThePointerLeaves() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2); _ = m.tick(now: 3)
        m.apply([], now: 3.1, pop: .allowed)            // the core cleared what was seen
        XCTAssertEqual(m.phase, .expanded(since: 2))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
        m.pointerLeft()
        XCTAssertEqual(m.phase, .hidden)
    }

    func testALineArrivingWhileExpandedIsAddedOnTopAndStaysOpen() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        m.apply([line(1), line(2)], now: 2.5, pop: .allowed)
        XCTAssertEqual(m.phase, .expanded(since: 2))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
    }

    func testPendingQuestionKeepsTheBadgeAfterLeaving() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2); _ = m.tick(now: 3)
        m.apply([line(2)], now: 3.1, pop: .allowed)
        m.pointerLeft()
        XCTAssertEqual(m.phase, .badge); XCTAssertEqual(m.count, 1)
        XCTAssertEqual(m.shown.map(\.id), [2])
    }

    func testAwayHoldsThePopAndReturnPopsOnce() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .hold)
        m.apply([line(1), line(2)], now: 2, pop: .hold)
        XCTAssertEqual(m.phase, .badge)
        m.back(now: 100, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 106))
        _ = m.tick(now: 106)
        m.back(now: 200, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)                 // nothing new: no second pop
    }

    func testReturnWithNothingLeftDoesNotPop() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .hold)
        m.apply([], now: 2, pop: .hold)                 // heard elsewhere while away
        m.back(now: 100, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testBadgeOnlyNeverPops() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .badgeOnly)
        XCTAssertEqual(m.phase, .badge)
    }

    func testEmptyListHides() {
        var m = UpdatesBubbleModel(); m.apply([line(1)], now: 0, pop: .allowed)
        m.apply([], now: 1, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
        XCTAssertEqual(m.count, 0)
    }

    func testMoreCountsBeyondMaxLines() {
        var m = UpdatesBubbleModel(popSecs: 6, maxLines: 2)
        m.apply((1...5).map(line), now: 0, pop: .allowed)
        XCTAssertEqual(m.shown.map(\.id), [5, 4]); XCTAssertEqual(m.more, 3)
        XCTAssertEqual(m.count, 5)
    }

    // M2: a line added while open, after the first seen call, is reported once shown 1 s.
    func testALineAddedAfterTheSeenCallIsReportedOnceShownOneSecond() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        XCTAssertEqual(m.tick(now: 3), [1])
        m.apply([line(2)], now: 3.5, pop: .allowed)      // the core dropped 1; 2 is new
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
        XCTAssertEqual(m.tick(now: 4.4), [], "2 has been shown 0.9 s")
        XCTAssertEqual(m.tick(now: 4.5), [2])
        XCTAssertEqual(m.tick(now: 6), [], "each line once")
    }

    func testANewExpansionReportsAgain() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2); XCTAssertEqual(m.tick(now: 3), [1])
        m.pointerLeft()                                  // 1 is a kept question, still listed
        m.pointerEntered(now: 10); XCTAssertEqual(m.tick(now: 11), [1])
    }

    // M1: hover counts only while the bubble can be seen.
    func testHoverCountsOnlyWhenTheBubbleIsVisible() {
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0, alreadyInside: false))
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0.05, alreadyInside: false))
        XCTAssertTrue(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0.5, alreadyInside: false))
        XCTAssertTrue(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0, alreadyInside: true),
                      "already open under the pointer: it is drawn at full opacity")
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: false, alpha: 1, alreadyInside: true))
    }
}
