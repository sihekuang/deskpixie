import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `src-tauri/src/canvas/window.rs` tests (4 of 4), plus the Swift-only
/// AppKit ↔ top-left conversion, the window title and the saved-geometry lookup.
final class CanvasWindowTests: XCTestCase {
    private let screen = CanvasRect(x: 0, y: 25, w: 1440, h: 875)

    private func win(_ x: Double, _ y: Double, _ width: Double, _ height: Double) -> CanvasGeometry {
        CanvasGeometry(x: x, y: y, width: width, height: height)
    }

    // MARK: window.rs

    func testAVisibleWindowIsKept() {
        XCTAssertEqual(CanvasPlacement.place(win(100, 100, 960, 720), areas: [screen]), win(100, 100, 960, 720))
    }

    func testAWindowOnAnUnpluggedMonitorIsDropped() {
        XCTAssertNil(CanvasPlacement.place(win(3000, 100, 960, 720), areas: [screen]))
        XCTAssertNil(CanvasPlacement.place(win(1400, 100, 960, 720), areas: [screen]), "only 40 px visible")
        XCTAssertNil(CanvasPlacement.place(win(-20000, -20000, 960, 720), areas: [screen]))
        XCTAssertNil(CanvasPlacement.place(win(100, 100, 960, 720), areas: []))
    }

    func testOversizedWindowsAreClampedAndTheTitleBarStaysVisible() {
        XCTAssertEqual(CanvasPlacement.place(win(-200, -50, 5000, 4000), areas: [screen]), win(-200, 25, 1440, 875))
    }

    func testParkedGeometryIsNotPlausible() {
        XCTAssertFalse(CanvasPlacement.plausible(win(-32000, -32000, 160, 28)))
        XCTAssertFalse(CanvasPlacement.plausible(win(.nan, 0, 800, 600)))
        XCTAssertTrue(CanvasPlacement.plausible(win(0, 0, 800, 600)))
    }

    // MARK: Swift-only

    func testSmallWorkAreasClampTheMinimumSizeToTheArea() {
        let tiny = CanvasRect(x: 0, y: 0, w: 400, h: 300)
        XCTAssertEqual(CanvasPlacement.place(win(0, 0, 200, 150), areas: [tiny]), win(0, 0, 400, 300))
        XCTAssertEqual(CanvasPlacement.place(win(0, 0, 200, 150), areas: [screen]), win(0, 25, 480, 360))
    }

    func testAppKitRectsConvertToTopLeftAndBack() {
        // Primary 1440×900; a second screen above-left of it.
        let r = CanvasScreenSpace.topLeft(CGRect(x: 0, y: 0, width: 1440, height: 875), primaryMaxY: 900)
        XCTAssertEqual(r, CanvasRect(x: 0, y: 25, w: 1440, h: 875))
        let above = CanvasScreenSpace.topLeft(CGRect(x: -1920, y: 900, width: 1920, height: 1080), primaryMaxY: 900)
        XCTAssertEqual(above, CanvasRect(x: -1920, y: -1080, w: 1920, h: 1080))
        // A frame whose top-left is (100, 100) and which is 748 tall (720 content + 28 title bar).
        let origin = CanvasScreenSpace.appKitOrigin(topLeftX: 100, topLeftY: 100, frameHeight: 748, primaryMaxY: 900)
        XCTAssertEqual(origin, CGPoint(x: 100, y: 52))
        XCTAssertEqual(CanvasScreenSpace.topLeft(CGRect(origin: origin, size: CGSize(width: 960, height: 748)),
                                                 primaryMaxY: 900),
                       CanvasRect(x: 100, y: 100, w: 960, h: 748))
    }

    func testTitles() {
        XCTAssertEqual(CanvasWindowTitle.make("Report"), "Canvas — Report")
        XCTAssertEqual(CanvasWindowTitle.make(""), "Canvas")
        XCTAssertEqual(CanvasWindowTitle.make("  "), "Canvas")
        XCTAssertEqual(CanvasWindowTitle.make("Canvas"), "Canvas", "already formatted (empty state)")
        XCTAssertEqual(CanvasWindowTitle.make("Canvas — Report"), "Canvas — Report", "already formatted")
    }

    func testSavedGeometryIsReadFromTheSettingsSnapshot() {
        let snap = #"{"platform":"macos","values":{"canvas":{"autoOpen":true,"keep":50,"window":{"x":10,"y":20.5,"width":800,"height":600}}},"seq":3}"#
        XCTAssertEqual(CanvasSettingsValues.window(fromSnapshot: Data(snap.utf8)), win(10, 20.5, 800, 600))
        let none = #"{"values":{"canvas":{"autoOpen":true,"keep":50,"window":null}}}"#
        XCTAssertNil(CanvasSettingsValues.window(fromSnapshot: Data(none.utf8)))
        XCTAssertNil(CanvasSettingsValues.window(fromSnapshot: Data("{}".utf8)))
        XCTAssertNil(CanvasSettingsValues.window(fromSnapshot: Data("garbage".utf8)))
    }

    func testGeometryEncodesAsTheCanvasGeometryArgs() throws {
        let data = try JSONEncoder().encode(win(1, 2, 3, 4))
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Double])
        XCTAssertEqual(obj, ["x": 1, "y": 2, "width": 3, "height": 4])
    }
}
