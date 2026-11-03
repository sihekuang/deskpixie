import XCTest

/// The switch on your session's row in the list under Pixie (#431): a stand-in session of yours
/// ("alpha", `UITestApp.startSession`) is turned off, and a turn it ends then never reaches the
/// chat window; turned back on, the next one does. Run with
/// `make -C mac uitest DESKPIXIE_UITEST_SESSIONS=1`.
@MainActor
final class SessionSwitchUITests: XCTestCase {
    private var harness: UITestApp?

    override func setUp() async throws {
        continueAfterFailure = false
        try XCTSkipUnless(UITestApp.withSessions, "set DESKPIXIE_UITEST_SESSIONS=1 to run the connected-sessions tests")
    }

    override func tearDown() async throws {
        if let harness {
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
    }

    /// Clicks the chip under Pixie until its list shows alpha's switch.
    private func openSwitch(_ app: XCUIApplication) -> XCUIElement? {
        let chip = app.descendants(matching: .any)["roster.chip"]
        guard chip.waitForExistence(timeout: 60) else { return nil }
        let toggle = app.descendants(matching: .any).matching(identifier: "roster.switch").firstMatch
        let deadline = Date().addingTimeInterval(30)
        while Date() < deadline {
            if toggle.exists { return toggle }
            chip.click()
            if toggle.waitForExistence(timeout: 2) { return toggle }
        }
        return nil
    }

    /// Waits for the switch to say what a click would do now.
    private func waitForLabel(_ toggle: XCUIElement, _ label: String) -> Bool {
        let deadline = Date().addingTimeInterval(15)
        while Date() < deadline {
            if toggle.label == label { return true }
            Thread.sleep(forTimeInterval: 0.25)
        }
        return false
    }

    func testASessionTurnedOffSendsTheChatNothingUntilItsBackOn() throws {
        let h = try UITestApp(agent: true, settings: [
            "notifications": ["sessionTurns": true, "sessionTurnsMinSeconds": 0, "bubble": ["enabled": false]],
        ])
        harness = h
        h.launch()
        try h.startSession(description: "alpha")

        guard let toggle = openSwitch(h.app) else { return XCTFail("no switch on alpha's row") }
        XCTAssertEqual(toggle.label, "Turn Pixie off for alpha")
        toggle.click()
        XCTAssertTrue(waitForLabel(toggle, "Turn Pixie back on for alpha"), "the switch didn't turn off: \(toggle.label)")

        let hidden = "Secret work while Pixie is off."
        try h.endTurn("alpha", reply: hidden)
        let window = h.openChat()
        XCTAssertFalse(window.waitForText(hidden, timeout: 8), "a turn of a session that is off reached the chat")

        guard let again = openSwitch(h.app) else { return XCTFail("no switch on alpha's row") }
        again.click()
        XCTAssertTrue(waitForLabel(again, "Turn Pixie off for alpha"), "the switch didn't turn back on: \(again.label)")
        let shown = "Back on and done."
        try h.endTurn("alpha", reply: shown)
        XCTAssertTrue(h.openChat().waitForText(shown, timeout: 30), "alpha's turn after it came back on isn't in the thread")
        XCTAssertFalse(h.app.descendants(matching: .any)["roster.notice"].exists, "no failure line")
    }
}
