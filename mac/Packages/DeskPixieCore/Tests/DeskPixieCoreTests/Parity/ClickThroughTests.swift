import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// V-031: the 60 Hz cursor poll's decision (actions.rs `spawn_proximity`), extracted from
/// PixieController: click-through wherever Pixie never draws, never mid-press or mid-drag, and
/// the proximity fade target.
final class ClickThroughTests: XCTestCase {
    private func fixture(_ name: String, _ ext: String) throws -> URL {
        try XCTUnwrap(Bundle.module.url(forResource: name, withExtension: ext, subdirectory: "Fixtures"))
    }

    private func bitMask() throws -> HitMask {
        let pack = try XCTUnwrap(PackPayload.decode(Data(contentsOf: fixture("bit-pack", "json")))).pack
        let sheet = try XCTUnwrap(SpriteSheet(url: fixture("bit-sheet", "png")))
        return HitMask.union(pack: pack, sheet: sheet)
    }

    /// 2×2 cell: only (0,0) is Pixie.
    private let tiny = HitMask(width: 2, height: 2, bits: Data([0b0001]))!
    /// Scale 8: the window is 16×16 points at (100, 200) in desktop space (y down).
    private let window = CGRect(x: 100, y: 200, width: 16, height: 16)

    private func decide(_ cursor: CGPoint, mask: HitMask? = nil, window w: CGRect? = nil, active: Bool = false,
                        down: Bool = false, mirrored: Bool = false, fade: Bool = true) -> ClickThrough.Decision {
        ClickThrough.decide(cursor: cursor, window: w ?? window, mask: mask ?? tiny, pointerActive: active,
                            leftButtonDown: down, mirrored: mirrored, fadeEnabled: fade, fadeRadius: 100)
    }

    func testOverBitTakesClicksElsewhereClickThrough() {
        let on = decide(CGPoint(x: 104, y: 204))
        XCTAssertFalse(on.ignoresMouseEvents)
        XCTAssertTrue(on.overBit)
        XCTAssertEqual(on.opacity, 1, "fully visible while over Pixie")
        let off = decide(CGPoint(x: 112, y: 212))
        XCTAssertTrue(off.ignoresMouseEvents, "transparent pixel of the window")
        XCTAssertFalse(off.overBit)
        XCTAssertEqual(off.opacity, 0, "inside the window rect: within 30% of the radius")
        XCTAssertTrue(decide(CGPoint(x: 400, y: 400)).ignoresMouseEvents, "outside the window")
    }

    func testScaleMapsWindowPointsToCellPixels() {
        // Cell pixel (0,0) spans window points [0, 8) at scale 8; (1,0) starts at 8.
        XCTAssertFalse(decide(CGPoint(x: 107.9, y: 207.9)).ignoresMouseEvents)
        XCTAssertTrue(decide(CGPoint(x: 108, y: 200)).ignoresMouseEvents)
        // Same mask at scale 4 (8×8 window).
        let small = CGRect(x: 100, y: 200, width: 8, height: 8)
        XCTAssertFalse(decide(CGPoint(x: 103.9, y: 203.9), window: small).ignoresMouseEvents)
        XCTAssertTrue(decide(CGPoint(x: 104, y: 200), window: small).ignoresMouseEvents)
    }

    func testMirroredWindowFlipsColumns() {
        XCTAssertTrue(decide(CGPoint(x: 104, y: 204), mirrored: true).ignoresMouseEvents)
        XCTAssertFalse(decide(CGPoint(x: 112, y: 204), mirrored: true).ignoresMouseEvents)
    }

    func testPressOrDragKeepsTheWindowClickableUntilButtonsAreUp() {
        let off = CGPoint(x: 112, y: 212)
        let held = decide(off, active: true, down: true)
        XCTAssertFalse(held.ignoresMouseEvents, "mid-press / mid-drag never click-through")
        XCTAssertTrue(held.pointerActive)
        XCTAssertEqual(held.opacity, 1)
        let released = decide(off, active: true, down: false)
        XCTAssertFalse(released.pointerActive, "all buttons up ends the press")
        XCTAssertTrue(released.ignoresMouseEvents)
        XCTAssertFalse(decide(off, active: false, down: true).pointerActive, "a press elsewhere is not ours")
    }

    func testFadeOffKeepsFullOpacity() {
        XCTAssertEqual(decide(CGPoint(x: 112, y: 212), fade: false).opacity, 1)
        XCTAssertEqual(decide(CGPoint(x: 1000, y: 1000)).opacity, 1, "beyond the radius")
    }

    func testBitsMaskCoversTheBobOffset() throws {
        let mask = try bitMask()
        let s: CGFloat = 8
        let w = CGRect(x: 0, y: 0, width: 32 * s, height: 32 * s)
        // The lowest body row of any frame, plus bob.px (1) below it, is still Pixie.
        let lowest = try XCTUnwrap((0..<32).reversed().first { y in (0..<32).contains { mask.contains(x: $0, y: y) } })
        let col = try XCTUnwrap((0..<32).first { mask.contains(x: $0, y: lowest) })
        XCTAssertTrue(mask.contains(x: col, y: lowest - 1), "the bob smear continues the body downwards")
        let at = CGPoint(x: (CGFloat(col) + 0.5) * s, y: (CGFloat(lowest) + 0.5) * s)
        XCTAssertFalse(decide(at, mask: mask, window: w).ignoresMouseEvents)
        XCTAssertTrue(decide(CGPoint(x: 0.5 * s, y: 0.5 * s), mask: mask, window: w).ignoresMouseEvents, "corner")
    }
}
