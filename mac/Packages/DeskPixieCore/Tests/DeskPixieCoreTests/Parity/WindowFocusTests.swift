import AppKit
import XCTest
@testable import DeskPixieCore

/// Live finding: Settings… (and a tray-opened canvas) appeared behind other apps' windows.
/// The menu-bar app must become a regular app, activate, and bring the window to the front.
@MainActor
final class WindowFocusTests: XCTestCase {
    private final class Log {
        var lines: [String] = []
    }

    private final class FakeApp: AppActivation {
        let log: Log
        init(_ log: Log) { self.log = log }
        func setActivationPolicy(_ p: NSApplication.ActivationPolicy) -> Bool {
            log.lines.append(p == .regular ? "policy:regular" : "policy:other")
            return true
        }
        func activate() { log.lines.append("activate") }
    }

    private final class FakeWindow: FocusableWindow {
        let log: Log
        init(_ log: Log) { self.log = log }
        func makeKeyAndOrderFront(_ sender: Any?) { log.lines.append("makeKey") }
        func orderFrontRegardless() { log.lines.append("orderFront") }
    }

    func testBringForwardActivatesThenFrontsTheWindow() {
        let log = Log()
        WindowFocus.bringForward(FakeWindow(log), app: FakeApp(log), showInDock: true)
        XCTAssertEqual(log.lines, ["policy:regular", "activate", "makeKey", "orderFront"])
    }

    func testCanvasFocusStaysAnAccessoryApp() {
        let log = Log()
        WindowFocus.bringForward(FakeWindow(log), app: FakeApp(log), showInDock: false)
        XCTAssertEqual(log.lines, ["activate", "makeKey", "orderFront"])
    }

    func testNoWindowStillActivates() {
        let log = Log()
        WindowFocus.bringForward(nil, app: FakeApp(log), showInDock: true)
        XCTAssertEqual(log.lines, ["policy:regular", "activate"])
    }

    func testOpenSettingsActivatesOpensAndFrontsTheWindowOnTheNextTurn() {
        let log = Log()
        var later: [() -> Void] = []
        WindowFocus.openSettings(app: FakeApp(log), open: { log.lines.append("open") },
                                 findWindow: { FakeWindow(log) }, defer: { later.append($0) })
        XCTAssertEqual(log.lines, ["policy:regular", "activate", "open"])
        XCTAssertEqual(later.count, 1, "the window exists only after SwiftUI has handled open")
        later.forEach { $0() }
        XCTAssertEqual(log.lines, ["policy:regular", "activate", "open", "policy:regular", "activate", "makeKey", "orderFront"])
    }

    func testRealTypesConform() {
        let _: any AppActivation = NSApplication.shared
        let _: any FocusableWindow = NSWindow()
    }
}
