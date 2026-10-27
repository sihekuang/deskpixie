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

    /// Settings… from the menu bar menu, and the window it opens.
    private func openSettings(_ h: UITestApp) -> XCUIElement {
        h.chooseFromMenuBar("Settings…", typing: "settings")
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
        field.typeText("always send")
        XCTAssertEqual(field.value as? String, "always send", "the field stopped taking keys")
        // Not on General, the page Settings opens on: only the search shows it.
        XCTAssertTrue(window.waitForText("Always send messages at once", timeout: 5), "no result for it")
    }
}
