import XCTest
@testable import DeskPixieCore

/// #286: a swipe rests on exactly one card.
final class DeckSnapTests: XCTestCase {
    func testASwipeRestsOnTheNearestCard() {
        XCTAssertEqual(DeckSnap.nearest(offset: 0, stride: 236), 0)
        XCTAssertEqual(DeckSnap.nearest(offset: 117, stride: 236), 0, "short of halfway: back")
        XCTAssertEqual(DeckSnap.nearest(offset: 119, stride: 236), 1, "past halfway: the next")
        XCTAssertEqual(DeckSnap.nearest(offset: 472, stride: 236), 2)
        XCTAssertEqual(DeckSnap.nearest(offset: -30, stride: 236), 0, "pulled past the first card")
        XCTAssertEqual(DeckSnap.nearest(offset: 50, stride: 0), 0)
    }
}
