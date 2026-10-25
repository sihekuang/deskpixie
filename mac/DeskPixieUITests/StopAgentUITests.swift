import AppKit
import XCTest

/// The stop button by a working agent in the session list under Pixie (#309): a stand-in
/// session of yours ("alpha", `UITestApp.startSession`) is put to work, and its stop button
/// interrupts it. The stand-in takes the Esc as claude does, so pix ends the turn and the button
/// goes, from the core's own status events. The chat window's stop button is in
/// `ChatSessionsUITests`. Run with `make -C mac uitest DESKPIXIE_UITEST_SESSIONS=1`.
@MainActor
final class StopAgentUITests: XCTestCase {
    private var harness: UITestApp?
    /// alpha's pix was frozen (SIGSTOP): tearDown thaws it before cleaning up.
    private var frozen = false

    override func setUp() async throws {
        continueAfterFailure = false
        try XCTSkipUnless(UITestApp.withSessions, "set DESKPIXIE_UITEST_SESSIONS=1 to run the connected-sessions tests")
    }

    override func tearDown() async throws {
        if frozen, let harness { harness.signalPix("alpha", SIGCONT) }
        frozen = false
        if let harness {
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
    }

    /// The app with the agent on (no coordinator until a message is sent) and alpha running in
    /// it. Every turn of a session is told in the thread, and no bubble shows it.
    private func start() throws -> UITestApp {
        let h = try UITestApp(agent: true, settings: [
            "notifications": ["sessionTurns": true, "sessionTurnsMinSeconds": 0, "bubble": ["enabled": false]],
        ])
        harness = h
        h.launch()
        try h.startSession(description: "alpha")
        return h
    }

    private func stopButtons(in element: XCUIElement, _ id: String) -> XCUIElementQuery {
        element.descendants(matching: .any).matching(identifier: id)
    }

    /// Clicks the chip under Pixie until its list is open.
    private func openList(_ app: XCUIApplication) -> Bool {
        let chip = app.descendants(matching: .any)["roster.chip"]
        guard chip.waitForExistence(timeout: 60) else { return false }
        let deadline = Date().addingTimeInterval(20)
        while Date() < deadline {
            chip.click()
            if app.descendants(matching: .any).matching(identifier: "roster.row").firstMatch.waitForExistence(timeout: 2) {
                return true
            }
        }
        return false
    }

    func testAWorkingSessionStopsFromTheSessionList() throws {
        let h = try start()
        XCTAssertTrue(openList(h.app), "the session list never opened")
        let stop = stopButtons(in: h.app, "roster.stop").firstMatch
        XCTAssertFalse(stop.exists, "idle: no stop button")

        try h.startTurn("alpha", prompt: "Run the slow tests")
        XCTAssertTrue(stop.waitForExistence(timeout: 20), "no stop button on the working row")
        XCTAssertEqual(stop.label, "Stop alpha")
        stop.click()

        let deadline = Date().addingTimeInterval(10)
        while !h.escaped("alpha") && Date() < deadline { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(h.escaped("alpha"), "alpha was never sent an Esc")
        XCTAssertTrue(stop.waitForGone(timeout: 20), "the row still offers stop once alpha is idle")
        XCTAssertFalse(h.app.descendants(matching: .any)["roster.notice"].exists, "no failure line")
    }

    /// alpha's pix is frozen under the click: still listed and working, but it answers nothing,
    /// so the stop times out and the line under the list says so. Deterministic: nothing in the
    /// list changes meanwhile.
    func testAStopThatCantReachItsSessionSaysSo() throws {
        let h = try start()
        XCTAssertTrue(openList(h.app), "the session list never opened")
        let stop = stopButtons(in: h.app, "roster.stop").firstMatch
        try h.startTurn("alpha", prompt: "Run the slow tests")
        XCTAssertTrue(stop.waitForExistence(timeout: 20), "no stop button on the working row")
        XCTAssertNotNil(h.signalPix("alpha", SIGSTOP), "alpha's pix not found")
        frozen = true
        stop.click()

        let notice = h.app.descendants(matching: .any)["roster.notice"]
        XCTAssertTrue(notice.waitForExistence(timeout: 10), "no failure line")
        XCTAssertEqual(notice.label, "Couldn't stop alpha: it didn't answer in time.")
        // Before it thaws: then pix types the Esc it was sent late (the core holds the next).
        XCTAssertFalse(h.escaped("alpha"), "nothing reached it")
    }
}
