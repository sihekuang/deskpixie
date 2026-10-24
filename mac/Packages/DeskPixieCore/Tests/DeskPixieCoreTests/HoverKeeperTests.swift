import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// #286: what shows beside Pixie stays up under a wandering pointer.
final class HoverKeeperTests: XCTestCase {
    private let pixie = CGRect(x: 100, y: 100, width: 100, height: 100)
    private let card = CGRect(x: 80, y: 0, width: 260, height: 80) // below, with a gap of 20

    func testTheRegionCoversTheFramesTheGapsAndAPadding() {
        let r = HoverKeeper.region([pixie, card])!
        XCTAssertTrue(r.contains(CGPoint(x: 150, y: 90)), "the gap between Pixie and the card")
        XCTAssertTrue(r.contains(CGPoint(x: 350, y: 40)), "within 16 pt of the card")
        XCTAssertFalse(r.contains(CGPoint(x: 360, y: 40)))
        XCTAssertNil(HoverKeeper.region([]))
        XCTAssertNil(HoverKeeper.region([.zero]))
    }

    /// The region reaches wherever a card or the deck could show, wherever the placement puts it.
    func testTheReachFollowsThePlacementNearTheScreensEdges() {
        let screen = CGRect(x: 0, y: 0, width: 2000, height: 1200)
        let size = CGSize(width: 128, height: 128)
        func region(_ origin: CGPoint) -> CGRect {
            HoverKeeper.region(HoverKeeper.reach(pixie: CGRect(origin: origin, size: size), screen: screen))!
        }
        // Mid-screen: the deck to the right at its widest, the card below at its tallest.
        var r = region(CGPoint(x: 800, y: 300))
        XCTAssertTrue(r.contains(CGPoint(x: 928 + 8 + UpdatesBubbleLayout.maxWidth + 10, y: 310)))
        XCTAssertTrue(r.contains(CGPoint(x: 864, y: 428 + 4 + ChipPlacement.textBoxHeight + 40 + 10)))
        XCTAssertFalse(r.contains(CGPoint(x: 700, y: 310)), "nothing shows to the left here")
        // Near the right edge: the deck goes to the left.
        r = region(CGPoint(x: 1800, y: 300))
        XCTAssertTrue(r.contains(CGPoint(x: 1800 - 8 - UpdatesBubbleLayout.maxWidth + 10, y: 310)))
        // Near the bottom: the card goes above Pixie.
        r = region(CGPoint(x: 800, y: 1000))
        XCTAssertTrue(r.contains(CGPoint(x: 864, y: 1000 - 4 - ChipPlacement.textBoxHeight)))
    }

    func testLeavingKeepsThingsUpForTheGraceThenLetsGo() {
        var k = HoverKeeper()
        XCTAssertFalse(k.keepsShowing(now: 0))
        k.pointer(at: CGPoint(x: 150, y: 90), frames: [pixie, card], now: 1)
        XCTAssertTrue(k.keepsShowing(now: 1))
        k.pointer(at: CGPoint(x: 500, y: 500), frames: [pixie, card], now: 2)
        XCTAssertFalse(k.inside)
        XCTAssertTrue(k.keepsShowing(now: 2.5))
        XCTAssertFalse(k.keepsShowing(now: 2 + HoverKeeper.grace + 0.01))
    }

    func testComingBackCancelsTheGrace() {
        var k = HoverKeeper()
        k.pointer(at: CGPoint(x: 150, y: 150), frames: [pixie], now: 0)
        k.pointer(at: CGPoint(x: 500, y: 500), frames: [pixie], now: 1)
        k.pointer(at: CGPoint(x: 150, y: 150), frames: [pixie], now: 1.3)
        k.pointer(at: CGPoint(x: 150, y: 150), frames: [pixie], now: 5)
        XCTAssertTrue(k.keepsShowing(now: 5))
        k.pointer(at: CGPoint(x: 500, y: 500), frames: [pixie], now: 6)
        XCTAssertTrue(k.keepsShowing(now: 6.5), "the grace starts again from the new exit")
    }

    func testASwipeKeepsThingsUpUntilItHasRestedForTheGrace() {
        var k = HoverKeeper()
        k.scroll(active: true, now: 0)
        XCTAssertTrue(k.keepsShowing(now: 10))
        k.scroll(active: false, now: 10)
        XCTAssertTrue(k.keepsShowing(now: 10.5))
        XCTAssertFalse(k.keepsShowing(now: 10 + HoverKeeper.grace + 0.01))
    }

    func testAPointerThatNeverCameInStartsNoGrace() {
        var k = HoverKeeper()
        k.pointer(at: CGPoint(x: 500, y: 500), frames: [pixie], now: 0)
        XCTAssertFalse(k.keepsShowing(now: 0))
    }
}
