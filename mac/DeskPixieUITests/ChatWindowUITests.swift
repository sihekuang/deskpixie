import AppKit
import XCTest

/// The chat window (#302), driven from the menu bar like a person would. Each test runs its own
/// DeskPixie on a fresh data folder (`UITestApp`); a thread is seeded through the store's own
/// file. Run with `make -C mac uitest` (the UITest build, under its own bundle ID: your own
/// DeskPixie can stay open); `DESKPIXIE_UITEST_LIVE=1` adds the tests that need herdr, claude and
/// the coordinator.
@MainActor
final class ChatWindowUITests: XCTestCase {
    private var harness: UITestApp?
    private var pasteboard: SavedPasteboard?

    override func setUp() async throws {
        continueAfterFailure = false
        pasteboard = SavedPasteboard()
    }

    override func tearDown() async throws {
        if let harness {
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
        // After the app is gone, so it never sees the user's clipboard come back as a new copy.
        pasteboard?.restore()
        pasteboard = nil
    }

    private func start(_ entries: [SeedEntry] = [], live: Bool = false, agent: Bool? = nil) throws -> UITestApp {
        let h = try UITestApp(live: live, agent: agent)
        harness = h
        if !entries.isEmpty { try h.seed(entries) }
        h.launch()
        return h
    }

    // MARK: The seeded thread

    private static let apiServer = SeedEntry.Who(kind: "session", name: "pix:api-server", label: "api-server",
                                                 yours: true, status: "working", color: 2)

    /// You, Pixie, the coordinator, a session of yours, and a permission it asked for that you
    /// answered: in time order, a minute or so ago.
    private static var thread: [SeedEntry] {
        let start: TimeInterval = 120
        return [
            SeedEntry(id: "u1", at: SeedEntry.ms(ago: start), who: .init(kind: "you"),
                      text: "Is the api-server build done?"),
            SeedEntry(id: "p1", at: SeedEntry.ms(ago: start - 10), who: .init(kind: "pixie"),
                      text: "Let me ask the coordinator."),
            SeedEntry(id: "c1", at: SeedEntry.ms(ago: start - 20), who: .init(kind: "coordinator"),
                      text: "api-server is running its tests now."),
            SeedEntry(id: "s1", at: SeedEntry.ms(ago: start - 30), who: apiServer,
                      text: "Tests passed. Ready to push."),
            SeedEntry(id: "r1", at: SeedEntry.ms(ago: start - 40), who: apiServer,
                      text: "git push origin fix-login",
                      request: .init(item: 7, kind: "permission", outcome: "You allowed git push")),
        ]
    }

    private func entry(_ id: String, in window: XCUIElement) -> XCUIElement {
        window.element("chat.entry.\(id)")
    }

    // MARK: Offline

    func testChatOpensFromTheMenuBar() throws {
        let h = try start()
        let window = h.openChat()
        XCTAssertEqual(window.title, "Pixie")
        XCTAssertTrue(window.element("chat.thread").exists)
        XCTAssertTrue(window.element("chat.composer").exists)
    }

    func testASeededThreadShowsWhoSaidWhat() throws {
        let h = try start(Self.thread)
        let window = h.openChat()

        let yours = entry("u1", in: window)
        XCTAssertTrue(yours.waitForExistence(timeout: 10))
        XCTAssertTrue(yours.waitForText("Is the api-server build done?", timeout: 5))

        let pixie = entry("p1", in: window)
        XCTAssertTrue(pixie.waitForText("Pixie", timeout: 5))
        XCTAssertTrue(pixie.waitForText("Let me ask the coordinator.", timeout: 5))

        let coordinator = entry("c1", in: window)
        XCTAssertTrue(coordinator.waitForText("Coordinator", timeout: 5))
        XCTAssertTrue(coordinator.waitForText("api-server is running its tests now.", timeout: 5))

        let session = entry("s1", in: window)
        XCTAssertTrue(session.waitForText("api-server", timeout: 5))
        XCTAssertTrue(session.waitForText("your session", timeout: 5))
        XCTAssertTrue(session.waitForText("Tests passed. Ready to push.", timeout: 5))
        XCTAssertTrue(session.element("chat.entry.reply").exists, "a session's message offers Reply…")

        // Yours sits on the right, the others on the left.
        XCTAssertGreaterThan(yours.text("Is the api-server build done?").frame.midX,
                             pixie.text("Let me ask the coordinator.").frame.midX)
    }

    /// An answered request is one outcome line: no card, no buttons, no pill. (An open one is
    /// the live test below: a seeded open request is "Answered elsewhere" once the agent starts.)
    func testAnAnsweredRequestShowsItsOutcomeLine() throws {
        let h = try start(Self.thread)
        let window = h.openChat()
        let outcome = entry("r1", in: window)
        XCTAssertTrue(outcome.waitForExistence(timeout: 10))
        XCTAssertTrue(outcome.waitForText("You allowed git push", timeout: 5))
        XCTAssertTrue(outcome.waitForText("api-server", timeout: 5))
        XCTAssertFalse(window.element("chat.request.allow").exists)
        XCTAssertFalse(window.element("chat.request.deny").exists)
        XCTAssertFalse(window.element("chat.needs.pill.r1").exists)
    }

    func testReplyPointsTheComposerAtTheSession() throws {
        let h = try start(Self.thread)
        let window = h.openChat()
        let session = entry("s1", in: window)
        XCTAssertTrue(session.waitForExistence(timeout: 10))

        session.element("chat.entry.reply").click()
        let chip = window.element("chat.replyChip")
        XCTAssertTrue(chip.waitForExistence(timeout: 5))
        XCTAssertTrue(chip.waitForText("Replying to api-server", timeout: 5))

        chip.buttons["Stop replying to api-server"].click()
        XCTAssertTrue(chip.waitForGone(timeout: 5), "× goes back to messaging Pixie")
    }

    func testClearHistoryAsksFirstAndEmptiesTheThread() throws {
        let h = try start(Self.thread)
        let window = h.openChat()
        XCTAssertTrue(entry("u1", in: window).waitForExistence(timeout: 10))

        chooseClearHistory(in: window, h.app)
        let sheet = window.sheets.firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 5))
        XCTAssertTrue(sheet.waitForText("Clear the chat?", timeout: 2))
        sheet.buttons["Cancel"].click()
        XCTAssertTrue(sheet.waitForGone(timeout: 5))
        XCTAssertTrue(entry("u1", in: window).exists, "Cancel keeps everything")
        XCTAssertTrue(entry("s1", in: window).exists)

        chooseClearHistory(in: window, h.app)
        XCTAssertTrue(sheet.waitForExistence(timeout: 5))
        sheet.buttons["Clear"].click()
        for id in ["u1", "p1", "c1", "s1", "r1"] {
            XCTAssertTrue(entry(id, in: window).waitForGone(timeout: 10), "\(id) is still there")
        }
    }

    /// The toolbar's More menu, then Clear History….
    private func chooseClearHistory(in window: XCUIElement, _ app: XCUIApplication) {
        let more = window.toolbars.menuButtons["More"].exists ? window.toolbars.menuButtons["More"]
            : window.toolbars.buttons["More"].exists ? window.toolbars.buttons["More"]
            : window.descendants(matching: .any).matching(NSPredicate(format: "label == 'More'")).firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no More menu in the toolbar")
        more.click()
        let clear = app.menuItems["chat.clearHistory"]
        XCTAssertTrue(clear.waitForExistence(timeout: 5))
        clear.click()
    }

    func testTheThreadIsThereAfterARelaunch() throws {
        let h = try start(Self.thread)
        var window = h.openChat()
        XCTAssertTrue(entry("s1", in: window).waitForExistence(timeout: 10))

        h.terminate()
        h.launch()
        window = h.openChat()
        let first = entry("u1", in: window), last = entry("s1", in: window)
        XCTAssertTrue(first.waitForExistence(timeout: 10))
        XCTAssertTrue(last.waitForExistence(timeout: 10))
        XCTAssertTrue(last.waitForText("Tests passed. Ready to push.", timeout: 5))
        XCTAssertLessThan(first.frame.minY, last.frame.minY, "the file's order is kept")
    }

    // MARK: The button above Pixie

    /// Waits for an element's accessibility label to read `label`.
    private func waitForLabel(_ element: XCUIElement, _ label: String, timeout: TimeInterval = 10) -> Bool {
        let reads = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label == %@", label), object: element)
        return XCTWaiter().wait(for: [reads], timeout: timeout) == .completed
    }

    /// A click shows the window, the next hides it, and so does ⌘⇧C; ⌘W closes it, and every
    /// way of putting it away turns the button back to "Show chat".
    func testTheChatButtonShowsAndHidesTheWindow() throws {
        let h = try start(Self.thread)
        let button = h.app.buttons["pixie.chatButton"]
        XCTAssertTrue(button.waitForExistence(timeout: 60), "no chat button above Pixie")
        XCTAssertTrue(waitForLabel(button, "Show chat"))

        button.click()
        XCTAssertTrue(h.chatWindow.waitForExistence(timeout: 15), "a click didn't show the window")
        XCTAssertTrue(entry("s1", in: h.chatWindow).waitForExistence(timeout: 10))
        XCTAssertTrue(waitForLabel(button, "Hide chat"))

        button.click()
        XCTAssertTrue(h.chatWindow.waitForGone(timeout: 10), "a second click didn't hide it")
        XCTAssertTrue(waitForLabel(button, "Show chat"))

        button.click()
        XCTAssertTrue(h.chatWindow.waitForExistence(timeout: 15), "it didn't show again")
        XCTAssertTrue(entry("s1", in: h.chatWindow).exists, "the thread is still there")
        XCTAssertTrue(waitForLabel(button, "Hide chat"))

        h.chatWindow.typeKey("c", modifierFlags: [.command, .shift])
        XCTAssertTrue(h.chatWindow.waitForGone(timeout: 10), "⌘⇧C didn't hide it")
        XCTAssertTrue(waitForLabel(button, "Show chat"))

        button.click()
        XCTAssertTrue(h.chatWindow.waitForExistence(timeout: 15))
        h.chatWindow.typeKey("w", modifierFlags: .command)
        XCTAssertTrue(h.chatWindow.waitForGone(timeout: 10), "⌘W didn't close it")
        XCTAssertTrue(waitForLabel(button, "Show chat"), "closing didn't turn the button back")
    }

    // MARK: The clipboard (drag and drop is left to the hand check: XCUITest can't drive it)

    private static func png() -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 16, pixelsHigh: 16, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        for x in 0..<16 { for y in 0..<16 { rep.setColor(.systemBlue, atX: x, y: y) } }
        return rep.representation(using: .png, properties: [:])!
    }

    private func copyImage() {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setData(Self.png(), forType: .png)
    }

    private func copyText(_ text: String) {
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setString(text, forType: .string)
    }

    /// An attachment's chip: its × is "Remove <name>".
    private func chips(in window: XCUIElement) -> XCUIElementQuery {
        window.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Remove '"))
    }

    /// With the agent on (it keeps the attachments; off, the box says "The coordinator is off."),
    /// but no coordinator started: nothing is sent.
    func testTheHintRowAttachesACopiedImage() throws {
        let h = try start(agent: true)
        let window = h.openChat()
        XCTAssertTrue(window.element("chat.composer").waitForExistence(timeout: 10))
        XCTAssertEqual(chips(in: window).count, 0)

        // Copied after launch: only a new copy is offered.
        copyImage()
        XCTAssertTrue(window.waitForText("Screenshot copied", timeout: 10))
        // A link-style button: a link to accessibility.
        let attach = window.links["Attach"]
        XCTAssertTrue(attach.waitForExistence(timeout: 5), "no · Attach in the hint row")
        attach.click()
        XCTAssertTrue(chips(in: window).firstMatch.waitForExistence(timeout: 10), "no chip for the copy")
        XCTAssertTrue(attach.waitForGone(timeout: 5), "the hint goes once it is attached")
    }

    func testCmdVAttachesAnImageAndPastesText() throws {
        let h = try start(agent: true)
        let window = h.openChat()
        let composer = window.element("chat.composer")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))

        copyImage()
        composer.click()
        composer.typeKey("v", modifierFlags: .command)
        XCTAssertTrue(chips(in: window).firstMatch.waitForExistence(timeout: 10), "⌘V with an image attaches it")
        XCTAssertEqual(composer.value as? String ?? "", "", "the image isn't pasted as text")

        let words = "pasted by the UI test"
        copyText(words)
        composer.click()
        composer.typeKey("v", modifierFlags: .command)
        let deadline = Date().addingTimeInterval(5)
        while (composer.value as? String ?? "").contains(words) == false, Date() < deadline {
            Thread.sleep(forTimeInterval: 0.25)
        }
        XCTAssertTrue((composer.value as? String ?? "").contains(words), "⌘V with text pastes it")
        XCTAssertEqual(chips(in: window).count, 1, "text attaches nothing")
    }

    // MARK: Live (DESKPIXIE_UITEST_LIVE=1: herdr, claude and a coordinator; costs a little)

    /// A real permission: claude, wrapped in pix in this run's own herdr session, asks to run
    /// `touch`. Its pill goes to its card; Allow answers it and shrinks the card to its outcome.
    func testANeedsPillScrollsToItsCardAndAllowShrinksIt() throws {
        try XCTSkipUnless(UITestApp.live, "set DESKPIXIE_UITEST_LIVE=1 to run the live tests")
        guard let herdr = UITestApp.herdr else { throw XCTSkip("herdr isn't installed") }
        let h = try start(Self.thread, live: true)
        let window = h.openChat()

        let pane = try startSessionPane(h, herdr: herdr)
        let marker = h.root.appendingPathComponent("allowed-by-uitest").path
        let pix = UITestApp.builtApp.appendingPathComponent("Contents/Helpers/pix").path
        let prompt = "Run this exact shell command and nothing else: touch \(marker)"
        let command = "env -u PIX_SOCK -u PIX_SESSION DESKPIXIE_SUPPORT_DIR=\(quote(h.support.path)) "
            + "\(quote(pix)) claude --permission-mode default \(quote(prompt))"
        let ran = UITestApp.run(herdr, ["--session", h.session, "pane", "run", pane, command])
        XCTAssertEqual(ran.status, 0, "herdr pane run failed: \(ran.out)")

        let pill = window.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier BEGINSWITH 'chat.needs.pill.'")).firstMatch
        XCTAssertTrue(pill.waitForExistence(timeout: 180), "no pill for claude's permission")
        let id = String(pill.identifier.dropFirst("chat.needs.pill.".count))
        XCTAssertTrue(window.waitForText("1 needs you", timeout: 5))

        pill.click()
        let card = entry(id, in: window)
        XCTAssertTrue(card.waitForExistence(timeout: 10))
        let allow = card.element("chat.request.allow")
        XCTAssertTrue(allow.waitForExistence(timeout: 5))
        XCTAssertTrue(allow.isHittable, "the pill scrolls its card into view")
        XCTAssertTrue(card.element("chat.request.deny").exists)

        allow.click()
        XCTAssertTrue(pill.waitForGone(timeout: 15), "the strip loses the pill")
        XCTAssertTrue(card.waitForText("You allowed", timeout: 15), "the card shrinks to its outcome")
        XCTAssertFalse(card.element("chat.request.allow").exists)

        let deadline = Date().addingTimeInterval(90)
        while !FileManager.default.fileExists(atPath: marker), Date() < deadline {
            Thread.sleep(forTimeInterval: 1)
        }
        XCTAssertTrue(FileManager.default.fileExists(atPath: marker), "claude didn't run the allowed command")
    }

    func testASentMessageGetsTheCoordinatorsAnswer() throws {
        try XCTSkipUnless(UITestApp.live, "set DESKPIXIE_UITEST_LIVE=1 to run the live tests")
        let h = try start(live: true)
        let window = h.openChat()
        let composer = window.element("chat.composer")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))

        composer.click()
        composer.typeText("Reply with just the word pong.")
        composer.typeKey(.return, modifierFlags: [])

        let sent = window.text("Reply with just the word pong.")
        XCTAssertTrue(sent.waitForExistence(timeout: 10), "your message isn't in the thread")
        let thinking = window.element("chat.thinking"), step = window.element("chat.live.coordinator")
        XCTAssertTrue(thinking.waitForExistence(timeout: 15) || step.exists, "no Thinking… while it works")

        let answer = window.staticTexts.matching(NSPredicate(format: "label == 'Coordinator' OR value == 'Coordinator'"))
            .firstMatch
        XCTAssertTrue(answer.waitForExistence(timeout: 240), "no answer from the coordinator")
        XCTAssertTrue(thinking.waitForGone(timeout: 30))
    }

    // MARK: Live helpers

    /// A workspace in this run's own herdr session, in this repository (a folder claude already
    /// trusts), and its pane. The session is the one the app made, else one started here; either
    /// way `cleanUp` stops and deletes it.
    private func startSessionPane(_ h: UITestApp, herdr: String) throws -> String {
        let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().path
        let session = ["--session", h.session]
        func create() -> (status: Int32, out: String) {
            UITestApp.run(herdr, session + ["workspace", "create", "--cwd", repo, "--no-focus",
                                            "--label", "uitest", "--env", "DESKPIXIE_SUPPORT_DIR=\(h.support.path)"])
        }
        var made = create()
        if made.status != 0 {
            // Not up yet (the app starts it when its agent first needs it): start it here.
            let server = Process()
            server.executableURL = URL(fileURLWithPath: herdr)
            server.arguments = ["server"]
            server.environment = ProcessInfo.processInfo.environment.merging(["HERDR_SESSION": h.session]) { _, new in new }
            server.standardOutput = FileHandle.nullDevice
            server.standardError = FileHandle.nullDevice
            try server.run()
            let deadline = Date().addingTimeInterval(30)
            repeat {
                Thread.sleep(forTimeInterval: 1)
                made = create()
            } while made.status != 0 && Date() < deadline
        }
        XCTAssertEqual(made.status, 0, "herdr workspace create failed")
        guard let pane = Self.rootPane(made.out) else {
            XCTFail("no root pane in: \(made.out)")
            throw XCTSkip("no herdr pane")
        }
        Thread.sleep(forTimeInterval: 2) // its shell's prompt
        return pane
    }

    /// `.result.root_pane` of a `workspace create` reply: an id, or an object holding one.
    private static func rootPane(_ json: String) -> String? {
        guard let data = json.data(using: .utf8),
              let top = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let result = top["result"] as? [String: Any] else { return nil }
        if let id = result["root_pane"] as? String { return id }
        if let pane = result["root_pane"] as? [String: Any] {
            return (pane["pane_id"] ?? pane["id"]) as? String
        }
        return nil
    }

    private func quote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }
}
