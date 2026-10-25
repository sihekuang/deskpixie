import AppKit
import XCTest

/// The chat window with two sessions of yours connected (#302): the app's own pix, started as
/// `pix --description alpha claude` and `… beta claude`, around a stand-in claude that only ends
/// the turns a test gives it (`UITestApp.startSession`, `endTurn`). Nothing real runs: no claude,
/// no coordinator, no model, and the home is a temporary one, so ~/.claude.json gets no entry for
/// the run's folders (tearDown checks). Run with `make -C mac uitest DESKPIXIE_UITEST_SESSIONS=1`.
@MainActor
final class ChatSessionsUITests: XCTestCase {
    private var harness: UITestApp?
    private var pasteboard: SavedPasteboard?

    override func setUp() async throws {
        continueAfterFailure = false
        try XCTSkipUnless(UITestApp.withSessions, "set DESKPIXIE_UITEST_SESSIONS=1 to run the connected-sessions tests")
        pasteboard = SavedPasteboard()
    }

    override func tearDown() async throws {
        if let harness {
            XCTAssertEqual(Self.claudeProjects(under: harness.root), [],
                           "~/.claude.json got entries for this run's folders")
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
        pasteboard?.restore()
        pasteboard = nil
    }

    /// The keys in the real ~/.claude.json's projects that are this run's folder or inside it,
    /// under /tmp or /private/tmp (claude names folders by their real path). Only reads: other
    /// claude sessions on this Mac write the file all the time, and only the run's own folders
    /// are this test's business.
    private static func claudeProjects(under root: URL) -> [String] {
        guard let pw = getpwuid(getuid()), let dir = pw.pointee.pw_dir else { return [] }
        let file = URL(fileURLWithPath: String(cString: dir)).appendingPathComponent(".claude.json")
        guard let data = try? Data(contentsOf: file),
              let top = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let projects = top["projects"] as? [String: Any] else { return [] }
        let path = root.path
        let roots = [path, path.hasPrefix("/tmp/") ? "/private" + path : path]
        return projects.keys.filter { key in roots.contains { key == $0 || key.hasPrefix($0 + "/") } }.sorted()
    }

    /// The app (agent on, warm off), alpha and beta connected to it, and the window open with
    /// both in the @ list. Every turn of theirs is told, and no bubble reads it out of sight.
    private func start() throws -> (UITestApp, XCUIElement, XCUIElement) {
        let h = try UITestApp(agent: true, settings: [
            "notifications": ["sessionTurns": true, "sessionTurnsMinSeconds": 0, "bubble": ["enabled": false]],
        ])
        harness = h
        h.launch()
        try h.startSession(description: "alpha")
        try h.startSession(description: "beta")
        let window = h.openChat()
        let composer = window.element("chat.composer")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        composer.click()
        for name in ["alpha", "beta"] {
            XCTAssertTrue(waitForRecipient(name, in: window, composer), "\(name) never came to the list")
        }
        return (h, window, composer)
    }

    private func rows(_ window: XCUIElement) -> XCUIElementQuery {
        window.descendants(matching: .any).matching(identifier: "chat.mention.row")
    }

    /// Types `@` until the list shows this session (the roster lists it once pix connects), then
    /// takes the @ out again.
    private func waitForRecipient(_ label: String, in window: XCUIElement, _ composer: XCUIElement) -> Bool {
        let deadline = Date().addingTimeInterval(60)
        while Date() < deadline {
            composer.typeText("@")
            let row = rows(window).matching(NSPredicate(format: "label BEGINSWITH %@", label + ",")).firstMatch
            let there = row.waitForExistence(timeout: 3)
            composer.typeKey(.delete, modifierFlags: [])
            if there { return true }
            Thread.sleep(forTimeInterval: 1)
        }
        return false
    }

    private func draft(_ composer: XCUIElement) -> String { composer.value as? String ?? "" }

    private func pick(_ query: String, _ composer: XCUIElement) {
        composer.typeText("@" + query)
        composer.typeKey(.return, modifierFlags: [])
    }

    // MARK: The @ list and the one-recipient rule

    func testAtListsPixieAlphaAndBeta() throws {
        let (_, window, composer) = try start()
        composer.typeText("@")
        XCTAssertTrue(window.element("chat.mention.list").waitForExistence(timeout: 5))
        let labels = rows(window).allElementsBoundByIndex.map(\.label)
        XCTAssertEqual(labels.count, 3, "\(labels)")
        XCTAssertTrue(labels[0].hasPrefix("Pixie,"), "Pixie first: \(labels)")
        XCTAssertTrue(labels.contains { $0.hasPrefix("alpha,") } && labels.contains { $0.hasPrefix("beta,") })

        composer.typeText("al")
        let filtered = rows(window).allElementsBoundByIndex.map(\.label)
        XCTAssertEqual(filtered.count, 1, "\(filtered)")
        XCTAssertTrue(filtered.first?.hasPrefix("alpha,") ?? false)
    }

    func testATypedMentionBesideThePickTurnsSendOffAndAPickReplacesIt() throws {
        let (_, window, composer) = try start()
        pick("alp", composer)
        let chip = window.element("chat.replyChip")
        XCTAssertTrue(chip.waitForText("Replying to alpha", timeout: 5))

        composer.typeText("reply with ok @beta")
        let send = window.buttons["Send"]
        XCTAssertTrue(window.waitForText("This goes to alpha only. Remove @beta, or pick them instead.", timeout: 5))
        XCTAssertFalse(send.isEnabled, "Send is off")

        // The @beta being typed has its list open: Return picks beta, and the @beta goes.
        composer.typeKey(.return, modifierFlags: [])
        XCTAssertTrue(chip.waitForText("Replying to beta", timeout: 5), "beta replaced alpha")
        XCTAssertFalse(chip.text("alpha").exists)
        XCTAssertEqual(draft(composer), "reply with ok ")
        XCTAssertTrue(send.isEnabled)
    }

    func testTwoNamedWithNoPickTurnsSendOff() throws {
        let (_, window, composer) = try start()
        composer.typeText("@alpha @beta ")
        XCTAssertTrue(window.waitForText("Send to one at a time: pick @alpha or @beta.", timeout: 5))
        XCTAssertFalse(window.buttons["Send"].isEnabled)
        XCTAssertFalse(window.element("chat.replyChip").exists, "nothing was picked for you")
    }

    // MARK: The terminal button

    /// alpha's message has the terminal button by its name; a click brings forward the app alpha
    /// runs in. Here that's this test runner: the stand-in runs under `script`, started from it.
    func testTheTerminalButtonBringsAlphasTerminalForward() throws {
        let (h, window, _) = try start()
        let words = "The build is green."
        try h.endTurn("alpha", reply: words)
        XCTAssertTrue(window.text(words).waitForExistence(timeout: 30), "alpha's message isn't in the thread")
        let button = window.buttons.matching(identifier: "chat.entry.terminal")
            .matching(NSPredicate(format: "label == %@", "Open alpha's terminal")).firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 5), "no terminal button by alpha's name")
        XCTAssertNotEqual(NSWorkspace.shared.frontmostApplication?.processIdentifier, getpid(), "DeskPixie is in front")

        button.click()
        let me = getpid()
        let forward = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            NSWorkspace.shared.frontmostApplication?.processIdentifier == me
        }, object: nil)
        XCTAssertEqual(XCTWaiter().wait(for: [forward], timeout: 15), .completed,
                       "alpha's terminal app (the test runner) didn't come forward; in front: " +
                       "\(NSWorkspace.shared.frontmostApplication?.bundleIdentifier ?? "-")")
    }

    // MARK: A session that ends

    func testBetaEndingWhilePickedSaysSo() throws {
        let (h, window, composer) = try start()
        pick("bet", composer)
        XCTAssertTrue(window.element("chat.replyChip").waitForText("Replying to beta", timeout: 5))
        h.stopSession("beta")
        composer.typeText("reply with ok")
        XCTAssertTrue(window.waitForText("beta isn't running anymore.", timeout: 60), "no notice once beta ended")
        XCTAssertFalse(window.buttons["Send"].isEnabled)
        composer.typeKey(.return, modifierFlags: [])
        XCTAssertEqual(draft(composer), "reply with ok", "nothing went; the draft stays")
        XCTAssertTrue(window.waitForText("beta isn't running anymore.", timeout: 5))
    }
}
