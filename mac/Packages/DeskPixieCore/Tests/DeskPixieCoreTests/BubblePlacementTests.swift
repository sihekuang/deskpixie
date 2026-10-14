import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// Where the updates bubble sits beside Bit. Desktop coordinates (top-left origin, y down).
final class BubblePlacementTests: XCTestCase {
    private let bubble = CGSize(width: 240, height: 120)
    private let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)

    func testPrefersTheRightOfBitTopAligned() {
        let (f, side) = BubblePlacement.frame(mascot: CGRect(x: 100, y: 100, width: 64, height: 64),
                                              bubble: bubble, screen: screen)
        XCTAssertEqual(side, .right)
        XCTAssertEqual(f, CGRect(x: 172, y: 100, width: 240, height: 120))
    }

    func testFlipsLeftNearTheRightEdge() {
        let (f, side) = BubblePlacement.frame(mascot: CGRect(x: 1360, y: 100, width: 64, height: 64),
                                              bubble: bubble, screen: screen)
        XCTAssertEqual(side, .left)
        XCTAssertEqual(f.minX, 1360 - 8 - 240)
        XCTAssertLessThanOrEqual(f.maxX, screen.maxX)
    }

    func testClampsIntoTheScreenVertically() {
        let (f, _) = BubblePlacement.frame(mascot: CGRect(x: 100, y: 840, width: 64, height: 64),
                                           bubble: bubble, screen: screen)
        XCTAssertEqual(f.minY, 780)
        XCTAssertLessThanOrEqual(f.maxY, screen.maxY)
        let (top, _) = BubblePlacement.frame(mascot: CGRect(x: 100, y: -30, width: 64, height: 64),
                                             bubble: bubble, screen: screen)
        XCTAssertEqual(top.minY, 0, "Bit partly above the top edge: the bubble stays on screen")
    }

    func testWorksOnASecondScreenWithAnOffsetOrigin() {
        let second = CGRect(x: 1440, y: -200, width: 1920, height: 1080)
        let (f, side) = BubblePlacement.frame(mascot: CGRect(x: 3300, y: -200, width: 64, height: 64),
                                              bubble: bubble, screen: second)
        XCTAssertEqual(side, .left)
        XCTAssertTrue(second.contains(f), "\(f)")
    }
}
