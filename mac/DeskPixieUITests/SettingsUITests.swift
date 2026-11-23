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
        let field = window.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10), "no search field")
        field.click()
        field.typeText("always send")
        XCTAssertEqual(field.value as? String, "always send", "the field stopped taking keys")
        // Not on General, the page Settings opens on: only the search shows it.
        XCTAssertTrue(window.waitForText("Always send messages at once", timeout: 5), "no result for it")
    }

    /// #480: Voice is a list with a search field and ▶ per voice. The search finds a voice by its
    /// description, ▶ with no API key (this build has its own empty Keychain) says so under the
    /// row, and clicking a row picks the voice.
    func testTheVoiceListSearchesPreviewsAndPicks() throws {
        let h = try UITestApp(settings: ["voice": ["provider": "gemini-live", "voice": "Puck"]])
        harness = h
        h.launch()
        let window = openSettings(h)
        let tab = window.staticTexts["Voice"].firstMatch
        XCTAssertTrue(tab.waitForExistence(timeout: 10), "no Voice page in the sidebar")
        tab.click()
        let search = window.textFields["voice.search"]
        XCTAssertTrue(search.waitForExistence(timeout: 10), "no voice search field")
        let kore = window.buttons["voice.preview.Kore"]
        XCTAssertTrue(kore.waitForExistence(timeout: 5), "no ▶ for Kore")

        search.click()
        search.typeText("firm")
        XCTAssertTrue(window.waitForText("Firm", timeout: 5))
        XCTAssertFalse(window.buttons["voice.preview.Puck"].exists, "Puck (Upbeat) still listed for “firm”")
        kore.click()
        XCTAssertTrue(window.waitForText("Add the voice service's API key to hear previews.", timeout: 10),
                      "no word on why the preview can't play")

        search.doubleClick()
        search.typeKey(.delete, modifierFlags: [])
        search.typeText("kore")
        let row = window.buttons["voice.row.Kore"]
        XCTAssertTrue(row.waitForExistence(timeout: 5), "no row for Kore")
        row.click()
        XCTAssertTrue(window.waitForText("Changed: Voice", timeout: 10), "picking Kore asked for no restart")
        XCTAssertTrue(row.isSelected, "Kore isn't marked as the picked voice")
    }
}
