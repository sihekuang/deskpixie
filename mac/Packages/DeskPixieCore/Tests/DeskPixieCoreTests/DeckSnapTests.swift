import XCTest
@testable import DeskPixieCore

/// #286: a swipe rests on exactly one card.
final class DeckSnapTests: XCTestCase {
    func testALongFastFlickMovesExactlyOneCard() {
        XCTAssertEqual(DeckSnap.step(from: 0, moved: 2000, count: 5), 1)
        XCTAssertEqual(DeckSnap.step(from: 4, moved: -2000, count: 5), 3)
    }

    func testATinyDragStaysPut() {
        XCTAssertEqual(DeckSnap.step(from: 2, moved: 5, count: 5), 2)
        XCTAssertEqual(DeckSnap.step(from: 2, moved: -(DeckSnap.threshold - 1), count: 5), 2)
        XCTAssertEqual(DeckSnap.step(from: 2, moved: DeckSnap.threshold, count: 5), 3)
    }

    func testAStepStaysWithinTheDeck() {
        XCTAssertEqual(DeckSnap.step(from: 4, moved: 300, count: 5), 4, "past the last card: stays")
        XCTAssertEqual(DeckSnap.step(from: 0, moved: -300, count: 5), 0)
        XCTAssertEqual(DeckSnap.step(from: 9, moved: 300, count: 5), 4, "a start off the end is clamped")
        XCTAssertEqual(DeckSnap.step(from: 0, moved: 300, count: 0), 0)
    }

    func testASwipeRestsOnTheNearestCard() {
        XCTAssertEqual(DeckSnap.nearest(offset: 0, stride: 236), 0)
        XCTAssertEqual(DeckSnap.nearest(offset: 117, stride: 236), 0, "short of halfway: back")
        XCTAssertEqual(DeckSnap.nearest(offset: 119, stride: 236), 1, "past halfway: the next")
        XCTAssertEqual(DeckSnap.nearest(offset: 472, stride: 236), 2)
        XCTAssertEqual(DeckSnap.nearest(offset: -30, stride: 236), 0, "pulled past the first card")
        XCTAssertEqual(DeckSnap.nearest(offset: 50, stride: 0), 0)
    }
}
