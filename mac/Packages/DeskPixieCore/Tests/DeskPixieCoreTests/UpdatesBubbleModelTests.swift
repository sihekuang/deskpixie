import XCTest
@testable import DeskPixieCore

/// Spec §4 lifecycle of the updates bubble beside Pixie: hidden / popped / badge / expanded, the
/// 1 s "seen" rule, away → badge only → one pop on return. Times are seconds on one clock.
final class UpdatesBubbleModelTests: XCTestCase {
    private func line(_ id: UInt64) -> UpdateLine {
        UpdateLine(id: id, text: "line \(id)", kind: "reply", at: Int64(id), job: nil, worker: nil)
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
