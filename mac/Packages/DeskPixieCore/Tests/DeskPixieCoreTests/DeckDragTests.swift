import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// #286: a deck follows a two-finger swipe one card at most, and lands on one card.
final class DeckDragTests: XCTestCase {
    private func swipe(_ d: inout DeckDrag, dx: CGFloat, events: Int = 10, canBack: Bool = true,
                       canForward: Bool = true) -> DeckDrag.Verdict {
        _ = d.feed(.began, dx: 0, dy: 0, stride: 236, canBack: canBack, canForward: canForward)
        for _ in 0..<events {
            _ = d.feed(.changed, dx: dx / CGFloat(events), dy: 0, stride: 236, canBack: canBack, canForward: canForward)
        }
        return d.feed(.ended, dx: 0, dy: 0, stride: 236, canBack: canBack, canForward: canForward)
    }

    func testALongFastFlickMovesOneCardAndItsMomentumIsSwallowed() {
        var d = DeckDrag()
        XCTAssertEqual(swipe(&d, dx: -2000), .settle(step: 1), "fingers left: the later card")
        XCTAssertEqual(d.feed(.momentum, dx: -300, dy: 0, stride: 236, canBack: true, canForward: true), .take)
        _ = d.feed(.began, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true)
        XCTAssertEqual(d.feed(.momentum, dx: -300, dy: 0, stride: 236, canBack: true, canForward: true), .pass,
                       "a new gesture: momentum is no longer the deck's")
    }

    func testTheCardsFollowTheFingersAtMostOneCard() {
        var d = DeckDrag()
        _ = d.feed(.began, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true)
        for _ in 0..<20 { _ = d.feed(.changed, dx: 50, dy: 0, stride: 236, canBack: true, canForward: true) }
        XCTAssertEqual(d.offset, 236)
        XCTAssertEqual(d.feed(.ended, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true), .settle(step: -1))
        XCTAssertEqual(d.offset, 0)
    }

    func testATinyDragGoesBack() {
        var d = DeckDrag()
        XCTAssertEqual(swipe(&d, dx: 15), .settle(step: 0))
    }

    func testThereIsNoCardPastTheEnds() {
        var d = DeckDrag()
        XCTAssertEqual(swipe(&d, dx: 300, canBack: false), .settle(step: 0))
        _ = d.feed(.began, dx: 0, dy: 0, stride: 236, canBack: false, canForward: true)
        _ = d.feed(.changed, dx: 100, dy: 0, stride: 236, canBack: false, canForward: true)
        XCTAssertEqual(d.offset, 100 * DeckDrag.resistance, accuracy: 0.01, "it resists past the first card")
    }

    func testAnUpAndDownGestureIsLeftToTheText() {
        var d = DeckDrag()
        _ = d.feed(.began, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true)
        XCTAssertEqual(d.feed(.changed, dx: 1, dy: 10, stride: 236, canBack: true, canForward: true), .pass)
        XCTAssertEqual(d.feed(.changed, dx: 40, dy: 0, stride: 236, canBack: true, canForward: true), .pass)
        XCTAssertEqual(d.feed(.ended, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true), .pass)
        XCTAssertEqual(d.feed(.momentum, dx: 5, dy: 0, stride: 236, canBack: true, canForward: true), .pass)
        XCTAssertFalse(d.active)
    }

    func testEventsWithoutABeganAreLeftAlone() {
        var d = DeckDrag()
        XCTAssertEqual(d.feed(.changed, dx: 50, dy: 0, stride: 236, canBack: true, canForward: true), .pass)
        XCTAssertEqual(d.feed(.ended, dx: 0, dy: 0, stride: 236, canBack: true, canForward: true), .pass)
    }
}
