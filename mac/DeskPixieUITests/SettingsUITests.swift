import AppKit
import XCTest

/// The Settings window, opened from the menu bar as you would. Offline: no agent, no
/// coordinator, a temporary home.
@MainActor
final class SettingsUITests: XCTestCase {
    private var harness: UITestApp?

    override func setUp() async throws {
        continueAfterFailure = false
    }

    override func tearDown() async throws {
        if let harness {
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
    }

    /// Settings… from the menu bar menu, and the window it opens. Chosen by typing its name, as
    /// `openChatFromMenuBar` does: the app menu has a Settings… too, and the menu bar's is
    /// rebuilt while the core starts.
    private func openSettings(_ h: UITestApp) -> XCUIElement {
        let status = h.app.statusItems.firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 30), "no menu bar item")
        let placed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: status)
        XCTAssertEqual(XCTWaiter().wait(for: [placed], timeout: 30), .completed, "the menu bar item never took its place")
        status.click()
        XCTAssertTrue(status.menuItems["Settings…"].waitForExistence(timeout: 5), "no Settings… in the menu")
        for key in "settings" { h.app.typeKey(String(key), modifierFlags: []) }
        h.app.typeKey(.return, modifierFlags: [])
        let window = h.app.windows.matching(identifier: "settings").firstMatch
        XCTAssertTrue(window.waitForExistence(timeout: 15), "no Settings window")
        return window
    }

    /// #374: the search field takes a whole word as typed: the results that come with its first
    /// character don't take its focus away.
    func testSearchTakesAWholeWord() throws {
        let h = try UITestApp()
        harness = h
        h.launch()
        let window = openSettings(h)
        let field = window.textFields["Search settings"]
        XCTAssertTrue(field.waitForExistence(timeout: 10), "no search field")
        field.click()
        field.typeText("hotkey")
        XCTAssertEqual(field.value as? String, "hotkey", "the field stopped taking keys")
        XCTAssertTrue(window.waitForText("Voice hotkey", timeout: 5), "no result for it")
    }
}
