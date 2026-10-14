import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// C1: "a fullscreen app is in front" from the frontmost app's on-screen layer-0 window bounds
/// and the screens' frames, all in global display coordinates (top-left origin).
final class FullscreenTests: XCTestCase {
    private let main = CGRect(x: 0, y: 0, width: 1440, height: 900)
    private let second = CGRect(x: 1440, y: -200, width: 1920, height: 1080)

    func testAWindowCoveringAScreenIsFullscreen() {
        XCTAssertTrue(Fullscreen.isFullscreen(windows: [main], screens: [main, second]))
        XCTAssertTrue(Fullscreen.isFullscreen(windows: [CGRect(x: 10, y: 10, width: 100, height: 100), second],
                                              screens: [main, second]))
    }

    func testOrdinaryAndZoomedWindowsAreNot() {
        XCTAssertFalse(Fullscreen.isFullscreen(windows: [], screens: [main]))
        XCTAssertFalse(Fullscreen.isFullscreen(windows: [CGRect(x: 0, y: 25, width: 1440, height: 800)], screens: [main]),
                       "a zoomed window below the menu bar")
        XCTAssertFalse(Fullscreen.isFullscreen(windows: [main], screens: [second]), "another screen's size")
    }

    func testToleratesFractionalBounds() {
        XCTAssertTrue(Fullscreen.isFullscreen(windows: [CGRect(x: 0.4, y: 0, width: 1439.6, height: 900.3)],
                                              screens: [main]))
    }

    func testNoScreensIsNotFullscreen() {
        XCTAssertFalse(Fullscreen.isFullscreen(windows: [main], screens: []))
    }
}
