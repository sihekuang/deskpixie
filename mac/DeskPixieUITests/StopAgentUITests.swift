import AppKit
import XCTest

/// The stop button by a working agent (#309), offline: a stand-in session of yours ("alpha",
/// `UITestApp.startSession`) is put to work, and its stop button in the session list under
/// Pixie and in the chat window interrupts it. The stand-in takes the Esc as claude does, so
/// pix ends the turn and the button goes, from the core's own status events.
@MainActor
final class StopAgentUITests: XCTestCase {
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

    func testAWorkingSessionStopsFromTheChat() throws {
        let h = try start()
        let words = "First pass done, starting on the docs."
        try h.endTurn("alpha", reply: words)
        let window = h.openChat()
        XCTAssertTrue(window.waitForText(words, timeout: 30), "alpha's turn never came to the thread")
        let stop = stopButtons(in: window, "chat.entry.stop")
        XCTAssertEqual(stop.count, 0, "idle: no stop button")

        try h.startTurn("alpha", prompt: "Now the docs")
        XCTAssertTrue(stop.firstMatch.waitForExistence(timeout: 20), "no stop button by alpha's name")
        XCTAssertEqual(stop.count, 1, "only on alpha's newest entry")
        XCTAssertEqual(stop.firstMatch.label, "Stop alpha")
        let terminal = window.element("chat.entry.terminal")
        XCTAssertGreaterThan(stop.firstMatch.frame.minX, terminal.frame.minX, "right after the terminal button")
        XCTAssertLessThan(abs(stop.firstMatch.frame.midY - terminal.frame.midY), 8, "on the name's line")
        stop.firstMatch.click()

        let deadline = Date().addingTimeInterval(10)
        while !h.escaped("alpha") && Date() < deadline { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(h.escaped("alpha"), "alpha was never sent an Esc")
        XCTAssertTrue(stop.firstMatch.waitForGone(timeout: 20), "the header still offers stop once alpha is idle")
    }
}
