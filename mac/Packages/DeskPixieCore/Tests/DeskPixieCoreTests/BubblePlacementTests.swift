import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// Where the updates bubble sits beside Pixie. Desktop coordinates (top-left origin, y down).
final class BubblePlacementTests: XCTestCase {
    private let bubble = CGSize(width: 240, height: 120)
    private let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)

    func testPrefersTheRightOfBitTopAligned() {
        let (f, side) = BubblePlacement.frame(pixie: CGRect(x: 100, y: 100, width: 64, height: 64),
                                              bubble: bubble, screen: screen)
        XCTAssertEqual(side, .right)
        XCTAssertEqual(f, CGRect(x: 172, y: 100, width: 240, height: 120))
    }

    func testFlipsLeftNearTheRightEdge() {
        let (f, side) = BubblePlacement.frame(pixie: CGRect(x: 1360, y: 100, width: 64, height: 64),
                                              bubble: bubble, screen: screen)
        XCTAssertEqual(side, .left)
        XCTAssertEqual(f.minX, 1360 - 8 - 240)
        XCTAssertLessThanOrEqual(f.maxX, screen.maxX)
    }

    func testClampsIntoTheScreenVertically() {
        let (f, _) = BubblePlacement.frame(pixie: CGRect(x: 100, y: 840, width: 64, height: 64),
                                           bubble: bubble, screen: screen)
        XCTAssertEqual(f.minY, 780)
        XCTAssertLessThanOrEqual(f.maxY, screen.maxY)
        let (top, _) = BubblePlacement.frame(pixie: CGRect(x: 100, y: -30, width: 64, height: 64),
                                             bubble: bubble, screen: screen)
        XCTAssertEqual(top.minY, 0, "Pixie partly above the top edge: the bubble stays on screen")
    }

    func testWorksOnASecondScreenWithAnOffsetOrigin() {
        let second = CGRect(x: 1440, y: -200, width: 1920, height: 1080)
        let (f, side) = BubblePlacement.frame(pixie: CGRect(x: 3300, y: -200, width: 64, height: 64),
                                              bubble: bubble, screen: second)
        XCTAssertEqual(side, .left)
        XCTAssertTrue(second.contains(f), "\(f)")
    }

    // C2: the side comes from the open bubble's width, so the badge and the bubble that opens
    // from it sit on the same side of Pixie and the bubble covers the badge.

    private let badge = CGSize(width: 28, height: 22)

    /// Pixie at `Layout.defaultPosition`: 40 pt from the right edge.
    private var bitAtDefault: CGRect {
        let size = CGSize(width: 64, height: 64)
        let p = Layout.defaultPosition(primary: Layout.Area(x: 0, y: 0, w: screen.width, h: screen.height), size: size)
        return CGRect(origin: p, size: size)
    }

    func testAtTheDefaultSpotTheBadgeAndTheBubbleAreOnTheSameSide() {
        let w = bubble.width
        let (b, badgeSide) = BubblePlacement.frame(pixie: bitAtDefault, bubble: badge, screen: screen, sideWidth: w)
        let (f, bubbleSide) = BubblePlacement.frame(pixie: bitAtDefault, bubble: bubble, screen: screen, sideWidth: w)
        XCTAssertEqual(badgeSide, .left)
        XCTAssertEqual(bubbleSide, .left)
        XCTAssertTrue(f.contains(b), "the open bubble covers the badge it opened from: \(f) \(b)")
        XCTAssertEqual(b.maxX, bitAtDefault.minX - 8, "the badge sits next to Pixie")
        XCTAssertFalse(b.intersects(bitAtDefault))
    }

    func testOnTheRightTheBubbleAlsoCoversTheBadge() {
        let bit = CGRect(x: 100, y: 100, width: 64, height: 64)
        let (b, s1) = BubblePlacement.frame(pixie: bit, bubble: badge, screen: screen, sideWidth: bubble.width)
        let (f, s2) = BubblePlacement.frame(pixie: bit, bubble: bubble, screen: screen, sideWidth: bubble.width)
        XCTAssertEqual(s1, .right); XCTAssertEqual(s2, .right)
        XCTAssertEqual(b.minX, 172)
        XCTAssertTrue(f.contains(b))
    }

    func testNearTheBottomTheClampedBubbleStillCoversTheBadge() {
        let bit = CGRect(x: 1336, y: 860, width: 64, height: 64)
        let (b, _) = BubblePlacement.frame(pixie: bit, bubble: badge, screen: screen, sideWidth: bubble.width)
        let (f, _) = BubblePlacement.frame(pixie: bit, bubble: bubble, screen: screen, sideWidth: bubble.width)
        XCTAssertTrue(f.contains(b), "\(f) \(b)")
    }
}
