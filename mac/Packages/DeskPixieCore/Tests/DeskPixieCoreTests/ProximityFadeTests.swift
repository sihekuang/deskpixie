import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `src-tauri/src/proximity.rs` tests (opacity, distance, should_emit), plus the
/// `actions.rs` spawn_proximity rule (full opacity over Bit / while pressed / when disabled).
/// `cursor_cell` and `rescale` are not ported: AppKit gives points in one global space and
/// MascotController maps the cursor into the body cell itself.
final class ProximityFadeTests: XCTestCase {
    private let r = CGRect(x: 1000, y: 500, width: 256, height: 256)

    private func approx(_ a: CGFloat, _ b: CGFloat, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertEqual(a, b, accuracy: 1e-9, file: file, line: line)
    }

    func testFarAwayIsFullyVisible() {
        approx(ProximityFade.opacity(cursor: CGPoint(x: 0, y: 0), rect: r, radius: 150), 1.0)
    }

    func testExactlyAtRadiusIsFullyVisible() {
        approx(ProximityFade.opacity(cursor: CGPoint(x: 1000 - 150, y: 600), rect: r, radius: 150), 1.0)
    }

    func testInsideWindowIsInvisible() {
        approx(ProximityFade.opacity(cursor: CGPoint(x: 1100, y: 600), rect: r, radius: 150), 0.0)
    }

    func testWithinInnerCutoffIsInvisible() {
        // inner cutoff = 0.3 × 150 = 45
        approx(ProximityFade.opacity(cursor: CGPoint(x: 1000 - 40, y: 600), rect: r, radius: 150), 0.0)
    }

    func testHalfwayThroughTheBandIsQuadratic() {
        // band 45..150, midpoint 97.5 → t = 0.5 → 0.25
        approx(ProximityFade.opacity(cursor: CGPoint(x: 1000 - 97.5, y: 600), rect: r, radius: 150), 0.25)
    }

    func testDistanceUsesTheNearestCornerDiagonally() {
        approx(ProximityFade.distanceToRect(CGPoint(x: 1000 - 30, y: 500 - 40), r), 50.0)
    }

    func testWorksWithNegativeCoordinates() {
        let left = CGRect(x: -900, y: -300, width: 256, height: 256)
        approx(ProximityFade.opacity(cursor: CGPoint(x: -920, y: -200), rect: left, radius: 150), 0.0)
        approx(ProximityFade.opacity(cursor: CGPoint(x: -1200, y: -200), rect: left, radius: 150), 1.0)
    }

    func testZeroRadiusNeverHides() {
        approx(ProximityFade.opacity(cursor: CGPoint(x: 1100, y: 600), rect: r, radius: 0), 1.0)
    }

    func testEmitsOnlyMeaningfulChanges() {
        XCTAssertFalse(ProximityFade.shouldEmit(last: 0.500, next: 0.505))
        XCTAssertTrue(ProximityFade.shouldEmit(last: 0.50, next: 0.52))
        XCTAssertTrue(ProximityFade.shouldEmit(last: 0.005, next: 0.0))
        XCTAssertTrue(ProximityFade.shouldEmit(last: 0.995, next: 1.0))
        XCTAssertFalse(ProximityFade.shouldEmit(last: 1.0, next: 1.0))
    }

    // actions.rs: `if over || !fade { 1.0 } else { opacity(cursor, rect, radius * scale) }`
    func testTargetIsFullWhenOverBitPressedOrDisabled() {
        let inside = CGPoint(x: 1100, y: 600)
        approx(ProximityFade.target(cursor: inside, rect: r, radius: 150, enabled: true, overBit: true), 1.0)
        approx(ProximityFade.target(cursor: inside, rect: r, radius: 150, enabled: false, overBit: false), 1.0)
        approx(ProximityFade.target(cursor: inside, rect: r, radius: 150, enabled: true, overBit: false), 0.0)
        approx(ProximityFade.target(cursor: CGPoint(x: 1000 - 97.5, y: 600), rect: r, radius: 150,
                                    enabled: true, overBit: false), 0.25)
    }

    func testMonotonicOverTheRadius() {
        var last: CGFloat = -1
        for i in 0...300 {
            let o = ProximityFade.opacity(cursor: CGPoint(x: r.maxX + 150 * CGFloat(i) / 300, y: 600), rect: r, radius: 150)
            XCTAssertGreaterThanOrEqual(o, last)
            XCTAssertGreaterThanOrEqual(o, 0)
            XCTAssertLessThanOrEqual(o, 1.0)
            last = o
        }
    }
}
