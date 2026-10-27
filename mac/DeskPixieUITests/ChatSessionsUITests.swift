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

    /// alpha's message has the terminal button by its name, and a click asks the core for alpha's
    /// own session. alpha runs in no terminal app (`UITestApp.startSession`), so the core finds the
    /// session and says it can't find its terminal; a wrong address would say "no session of
    /// yours named …".
    func testTheTerminalButtonReachesAlphasSession() throws {
        let (h, window, _) = try start()
        let words = "The build is green."
        try h.endTurn("alpha", reply: words)
        XCTAssertTrue(window.waitForText(words, timeout: 30), "alpha's message isn't in the thread")
        let button = window.buttons.matching(identifier: "chat.entry.terminal")
            .matching(NSPredicate(format: "label == %@", "Open alpha's terminal")).firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 5), "no terminal button by alpha's name")

        button.click()
        let deadline = Date().addingTimeInterval(15)
        var lines: [String] = []
        repeat {
            Thread.sleep(forTimeInterval: 1)
            lines = statusLines(h)
        } while !lines.contains(where: { $0.contains("could not find the terminal of session process") }) && Date() < deadline
        XCTAssertTrue(lines.contains { $0.contains("could not find the terminal of session process") },
                      "focusSession didn't reach alpha: \(lines)")
        XCTAssertFalse(lines.contains { $0.contains("no session of yours named") }, "\(lines)")
    }

    // MARK: The stop button (#309)

    /// alpha at work has a stop button right after its terminal button, on its newest entry only
    /// (idle beta has none); a click sends alpha an Esc, which the stand-in takes as claude does,
    /// so pix ends the turn and the button goes.
    func testAWorkingSessionStopsFromTheChat() throws {
        let (h, window, _) = try start()
        let words = "First pass done, starting on the docs."
        try h.endTurn("alpha", reply: words)
        try h.endTurn("beta", reply: "Nothing to do.")
        XCTAssertTrue(window.waitForText(words, timeout: 30), "alpha's turn never came to the thread")
        XCTAssertTrue(window.waitForText("Nothing to do.", timeout: 30), "beta's turn never came to the thread")
        let stop = window.descendants(matching: .any).matching(identifier: "chat.entry.stop")
        XCTAssertEqual(stop.count, 0, "idle: no stop button")

        try h.startTurn("alpha", prompt: "Now the docs")
        XCTAssertTrue(stop.firstMatch.waitForExistence(timeout: 20), "no stop button by alpha's name")
        XCTAssertEqual(stop.count, 1, "only on alpha's newest entry")
        XCTAssertEqual(stop.firstMatch.label, "Stop alpha")
        let terminal = window.buttons.matching(identifier: "chat.entry.terminal")
            .matching(NSPredicate(format: "label == %@", "Open alpha's terminal")).firstMatch
        XCTAssertGreaterThan(stop.firstMatch.frame.minX, terminal.frame.minX, "right after the terminal button")
        XCTAssertLessThan(abs(stop.firstMatch.frame.midY - terminal.frame.midY), 8, "on the name's line")
        stop.firstMatch.click()

        let deadline = Date().addingTimeInterval(10)
        while !h.escaped("alpha") && Date() < deadline { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertTrue(h.escaped("alpha"), "alpha was never sent an Esc")
        XCTAssertFalse(h.escaped("beta"), "beta was sent an Esc")
        XCTAssertTrue(stop.firstMatch.waitForGone(timeout: 20), "the header still offers stop once alpha is idle")
    }

    /// The menu bar menu's status lines.
    private func statusLines(_ h: UITestApp) -> [String] {
        h.app.statusItems.firstMatch.click()
        let items = h.app.menuItems.allElementsBoundByIndex.map(\.title)
        h.app.typeKey(.escape, modifierFlags: [])
        return items.filter { $0.hasPrefix("Coordinator:") }
    }

    // MARK: A question of several picks (#364)

    /// Claude's menu tab of several picks (2.1.311), these ticked.
    private static func toppings(_ ticked: Set<Int>) -> String {
        let options = ["Cheese", "Ham", "Olives"].enumerated().map { i, o in
            "\(i == 0 ? "❯" : " ") \(i + 1). [\(ticked.contains(i + 1) ? "✔" : " ")] \(o)"
        }
        // A tick marks the tab answered at once, as Claude's does.
        return (["←  \(ticked.isEmpty ? "☐" : "☒") Pick  ✔ Submit  →", "Which toppings?"] + options + ["  4. [ ] Type something",
                "────────────────────", "  5. Chat about this", "Enter to select · ↑/↓ to navigate · Esc to cancel"])
            .joined(separator: "\r\n")
    }

    /// Its card's boxes are ticked, then Send: pix ticks each in alpha's menu and goes on (Tab),
    /// and Claude's review comes as the next card, whose Submit answers sends them.
    func testAQuestionOfSeveralPicksIsTickedAndSent() throws {
        let (h, window, _) = try start()
        let review = "←  ☒ Pick  ✔ Submit  →\r\nReview your answers\r\n ● Which toppings?\r\n   → Cheese, Olives\r\n"
            + "Ready to submit your answers?\r\n❯ 1. Submit answers\r\n  2. Cancel"
        try h.askMenu("alpha", input: ["questions": [["question": "Which toppings?", "header": "Pick", "multiSelect": true,
                                                       "options": [["label": "Cheese"], ["label": "Ham"], ["label": "Olives"]]]]],
                      screen: Self.toppings([]),
                      after: [("1", Self.toppings([1])), ("3", Self.toppings([1, 3])), ("\t", review),
                              ("1", "⏺ User answered Claude's questions:\r\n  ⎿  · Which toppings? → Cheese, Olives\r\n")])
        let send = window.element("chat.request.send")
        XCTAssertTrue(send.waitForExistence(timeout: 30), "no card with boxes for alpha's question")
        XCTAssertFalse(send.isEnabled, "Send waits for a box ticked")
        XCTAssertFalse(window.element("chat.request.reply").exists, "it takes no typed answer")
        window.element("chat.request.pick.1").click()
        window.element("chat.request.pick.3").click()
        XCTAssertTrue(send.isEnabled)
        send.click()

        let submit = window.buttons.matching(NSPredicate(format: "label == 'Submit answers'")).firstMatch
        XCTAssertTrue(submit.waitForExistence(timeout: 30), "no review card")
        XCTAssertTrue(window.waitForText("Which toppings? → Cheese, Olives", timeout: 5), "the review shows the picks")
        XCTAssertTrue(window.waitForText("You answered: Cheese, Olives", timeout: 5), "the first card's outcome")
        submit.click()
        let deadline = Date().addingTimeInterval(15)
        while h.keysTyped("alpha") != "13\t1" && Date() < deadline { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertEqual(h.keysTyped("alpha"), "13\t1", "the keys pix typed into alpha's menu")
        XCTAssertTrue(window.waitForText("You answered: Submit answers", timeout: 15))
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
