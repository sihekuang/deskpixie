import XCTest
@testable import DeskPixieCore

/// #286: a trackpad swipe steps the deck by one card, whatever its length.
final class DeckSwipeTests: XCTestCase {
    func testOneGestureStepsOneCard() {
        var s = DeckSwipe()
        XCTAssertEqual(s.feed(deltaX: 0, deltaY: 0, phase: .began), 0)
        XCTAssertEqual(s.feed(deltaX: 40, deltaY: 2, phase: .changed), -1)
        XCTAssertEqual(s.feed(deltaX: 200, deltaY: 0, phase: .changed), 0)
        XCTAssertEqual(s.feed(deltaX: 0, deltaY: 0, phase: .ended), 0)
        XCTAssertEqual(s.feed(deltaX: -40, deltaY: 0, phase: .began), 0)
        XCTAssertEqual(s.feed(deltaX: -40, deltaY: 0, phase: .changed), 1)
    }

    func testVerticalScrollingAndMomentumStepNothing() {
        var s = DeckSwipe()
        XCTAssertEqual(s.feed(deltaX: 40, deltaY: 80, phase: .changed), 0)
        XCTAssertEqual(s.feed(deltaX: 90, deltaY: 0, phase: .momentum), 0)
    }

    func testASlowSwipeAddsUpToOneStep() {
        var s = DeckSwipe()
        _ = s.feed(deltaX: 0, deltaY: 0, phase: .began)
        XCTAssertEqual(s.feed(deltaX: 12, deltaY: 0, phase: .changed), 0)
        XCTAssertEqual(s.feed(deltaX: 12, deltaY: 0, phase: .changed), 0)
        XCTAssertEqual(s.feed(deltaX: 12, deltaY: 0, phase: .changed), -1)
    }
}
