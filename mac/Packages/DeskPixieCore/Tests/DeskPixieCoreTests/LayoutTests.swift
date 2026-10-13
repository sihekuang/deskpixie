import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `src-tauri/src/layout.rs` tests (12 of 12), plus the AppKit ↔ desktop
/// coordinate conversion the Swift side needs (Rust/tao already worked in top-left, y-down).
final class LayoutTests: XCTestCase {
    private let size = CGSize(width: 256, height: 256)

    func testGrowingKeepsTheBottomCentre() {
        XCTAssertEqual(Layout.keepFeet(CGPoint(x: 1000, y: 500), old: CGSize(width: 256, height: 256),
                                       new: CGSize(width: 384, height: 384)), CGPoint(x: 936, y: 372))
    }

    func testShrinkingKeepsTheBottomCentre() {
        XCTAssertEqual(Layout.keepFeet(CGPoint(x: 1000, y: 500), old: CGSize(width: 256, height: 256),
                                       new: CGSize(width: 128, height: 128)), CGPoint(x: 1064, y: 628))
    }

    func testSameSizeDoesNotMove() {
        XCTAssertEqual(Layout.keepFeet(CGPoint(x: 10, y: 20), old: size, new: size), CGPoint(x: 10, y: 20))
    }

    private struct FakeDisplays: LayoutDisplays {
        var monitors: [Layout.Monitor]
        var primary: Layout.Area?
        func monitorList() -> [Layout.Monitor] { monitors }
        func primaryFrame() -> Layout.Area? { primary }
    }

    private let main = Layout.Area(x: 0, y: 0, w: 1440, h: 900)
    private let left = Layout.Area(x: -1920, y: -180, w: 1920, h: 1080)
    /// MAIN without a 25 pt menu bar and an 80 pt Dock.
    private let mainWork = Layout.Area(x: 0, y: 25, w: 1440, h: 795)

    /// Monitors whose work area is their whole frame.
    private func displays(_ frames: [Layout.Area]) -> FakeDisplays {
        FakeDisplays(monitors: frames.map { Layout.Monitor(frame: $0, work: $0) }, primary: frames.first)
    }

    private func mainWithDock() -> FakeDisplays {
        FakeDisplays(monitors: [Layout.Monitor(frame: main, work: mainWork)], primary: main)
    }

    private func restore(_ saved: CGPoint?, _ d: LayoutDisplays) -> CGPoint {
        Layout.restorePosition(saved, size: size, displays: d)
    }

    func testABitParkedOverTheDockOrMenuBarStaysPut() {
        // Centre (128+128, 840) is over the Dock: on the frame, outside the work area.
        XCTAssertEqual(restore(CGPoint(x: 128, y: 712), mainWithDock()), CGPoint(x: 128, y: 712))
        // Centre at y = 10, in the menu-bar strip.
        XCTAssertEqual(restore(CGPoint(x: 600, y: -118), mainWithDock()), CGPoint(x: 600, y: -118))
    }

    func testACentreOffEveryMonitorIsClampedIntoTheWorkArea() {
        // Below the screen: lands just above the Dock, not behind it.
        XCTAssertEqual(restore(CGPoint(x: 600, y: 2000), mainWithDock()), CGPoint(x: 600, y: 564))
        // Above the screen: lands just below the menu bar.
        XCTAssertEqual(restore(CGPoint(x: 600, y: -2000), mainWithDock()), CGPoint(x: 600, y: 25))
    }

    func testDefaultPositionIsTheBottomRightOfThePrimary() {
        XCTAssertEqual(Layout.defaultPosition(primary: main, size: size), CGPoint(x: 1144, y: 564))
        XCTAssertEqual(Layout.defaultPosition(primary: nil, size: size), CGPoint(x: 100, y: 100))
    }

    func testNoSavedPositionUsesTheDefault() {
        XCTAssertEqual(restore(nil, displays([main])), CGPoint(x: 1144, y: 564))
        var noPrimary = displays([main])
        noPrimary.primary = nil
        XCTAssertEqual(restore(nil, noPrimary), CGPoint(x: 1144, y: 564))
    }

    func testAPositionOnAConnectedMonitorIsKept() {
        let d = displays([main, left])
        XCTAssertEqual(restore(CGPoint(x: -900, y: 300), d), CGPoint(x: -900, y: 300))
        // Half hanging off the edge is still on screen (its centre is).
        XCTAssertEqual(restore(CGPoint(x: 1300, y: 700), d), CGPoint(x: 1300, y: 700))
    }

    func testAPositionOnAnUnpluggedMonitorIsPulledOntoTheNearestOne() {
        // Saved on the left display, which is now gone: lands on MAIN's left edge, same height.
        let d = displays([main])
        XCTAssertEqual(restore(CGPoint(x: -900, y: 300), d), CGPoint(x: 0, y: 300))
        // Far below and right: the bottom-right corner, fully visible.
        XCTAssertEqual(restore(CGPoint(x: 5000, y: 5000), d), CGPoint(x: 1184, y: 644))
    }

    func testTheNearestMonitorWins() {
        let right = Layout.Area(x: 1440, y: 0, w: 1920, h: 1080)
        XCTAssertEqual(restore(CGPoint(x: 3500, y: 100), displays([main, right])), CGPoint(x: 3104, y: 100))
    }

    func testAWindowLargerThanTheMonitorSticksToItsTopLeft() {
        let tiny = Layout.Area(x: 0, y: 0, w: 200, h: 200)
        XCTAssertEqual(restore(CGPoint(x: 900, y: 900), displays([tiny])), CGPoint(x: 0, y: 0))
    }

    func testWithoutMonitorInformationTheSavedPositionIsKept() {
        XCTAssertEqual(restore(CGPoint(x: -900, y: 300), displays([])), CGPoint(x: -900, y: 300))
    }

    // MARK: Swift-only: AppKit (bottom-left origin, y up) ↔ desktop (primary top-left, y down)

    func testAppKitRectsMapToTopLeftDesktopCoordinates() {
        // Primary 1440×900. A 256-pt window whose AppKit origin is (1144, 80) has its top at 336,
        // i.e. 564 down from the primary's top: Rust's default spot.
        let r = DesktopSpace.desktopRect(CGRect(x: 1144, y: 80, width: 256, height: 256), primaryMaxY: 900)
        XCTAssertEqual(r, CGRect(x: 1144, y: 564, width: 256, height: 256))
        XCTAssertEqual(DesktopSpace.appKitOrigin(desktopTopLeft: CGPoint(x: 1144, y: 564), size: size, primaryMaxY: 900),
                       CGPoint(x: 1144, y: 80))
        // A display above the primary has negative desktop y.
        XCTAssertEqual(DesktopSpace.desktopPoint(CGPoint(x: 10, y: 1000), primaryMaxY: 900), CGPoint(x: 10, y: -100))
    }

    func testAppKitAreaRoundTrips() {
        let a = DesktopSpace.area(CGRect(x: -1920, y: -100, width: 1920, height: 1080), primaryMaxY: 900)
        XCTAssertEqual(a, Layout.Area(x: -1920, y: -80, w: 1920, h: 1080))
        let back = DesktopSpace.appKitOrigin(desktopTopLeft: CGPoint(x: a.x, y: a.y),
                                             size: CGSize(width: a.w, height: a.h), primaryMaxY: 900)
        XCTAssertEqual(back, CGPoint(x: -1920, y: -100))
    }
}
