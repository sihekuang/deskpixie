@testable import DeskPixieCore
import XCTest

final class OverlayOrderTests: XCTestCase {
    private func order(expanded: Bool = false, popped: Bool = false, open: Bool = false, card: Bool = false) -> OverlayOrder {
        var o = OverlayOrder()
        o.bubbleExpanded = expanded
        o.bubblePopped = popped
        o.rosterOpen = open
        o.rosterCard = card
        return o
    }

    // #398: the bubble you opened was drawn under the reply card.
    func testTheBubbleYouPointAtIsOverTheReplyCard() {
        XCTAssertEqual(order(expanded: true, card: true).front, .bubble)
    }

    func testTheBubbleYouPointAtIsOverTheListToo() {
        XCTAssertEqual(order(expanded: true, open: true).front, .bubble)
    }

    // #91: the list you opened is over a bubble that popped up by itself.
    func testTheListYouOpenedIsOverAPoppedBubble() {
        XCTAssertEqual(order(popped: true, open: true).front, .roster)
    }

    func testAPoppedQuestionIsOverAReplyCard() {
        XCTAssertEqual(order(popped: true, card: true).front, .bubble)
    }

    func testAReplyCardIsOverTheBadge() {
        XCTAssertEqual(order(card: true).front, .roster)
    }

    func testNothingUpPutsNeitherInFront() {
        XCTAssertNil(order().front)
    }
}
