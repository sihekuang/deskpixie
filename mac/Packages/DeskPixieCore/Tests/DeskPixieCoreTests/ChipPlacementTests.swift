import CoreGraphics
import XCTest
@testable import DeskPixieCore

final class ChipPlacementTests: XCTestCase {
    private let screen = CGRect(x: 0, y: 0, width: 1440, height: 900)
    private let pixie = CGRect(x: 100, y: 100, width: 128, height: 128)

    func testCentredUnderPixie() {
        let (f, below) = ChipPlacement.frame(pixie: pixie, panel: CGSize(width: 60, height: 20), screen: screen, openHeight: 200)
        XCTAssertTrue(below)
        XCTAssertEqual(f, CGRect(x: 134, y: 232, width: 60, height: 20))
    }

    func testFlipsAboveWhenTheOpenListWouldNotFitBelow() {
        let low = CGRect(x: 100, y: 700, width: 128, height: 128)
        let (chip, below) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 60, height: 20), screen: screen, openHeight: 200)
        XCTAssertFalse(below, "decided by the open list's height, so it doesn't flip when the list opens")
        XCTAssertEqual(chip.maxY, 700 - 4)
        let (open, stillAbove) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 260, height: 200), screen: screen, openHeight: 200)
        XCTAssertFalse(stillAbove)
        XCTAssertEqual(open.maxY, 700 - 4)
    }

    func testAnOpenListTallerThanEstimatedKeepsItsSide() {
        // Opened below on the 22 pt/row estimate; the real list is taller and would not fit below.
        let low = CGRect(x: 100, y: 500, width: 128, height: 128)
        let (_, below) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 260, height: 300), screen: screen,
                                             openHeight: 200, keeping: true)
        XCTAssertTrue(below, "the side is kept while the list is open, so it never flips under the pointer")
        let (_, decided) = ChipPlacement.frame(pixie: low, panel: CGSize(width: 260, height: 300), screen: screen,
                                               openHeight: 200)
        XCTAssertFalse(decided)
    }

    func testClampedHorizontally() {
        let edge = CGRect(x: 0, y: 100, width: 128, height: 128)
        let (f, _) = ChipPlacement.frame(pixie: edge, panel: CGSize(width: 260, height: 200), screen: screen, openHeight: 200)
        XCTAssertEqual(f.minX, 0)
    }
}
