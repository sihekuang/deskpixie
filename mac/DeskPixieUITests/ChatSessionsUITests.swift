import AppKit
import XCTest

/// The chat window with two real sessions of yours connected (#302): `pix --description alpha
/// claude` and `… beta claude`, each in its own folder, in a private tmux server, on this run's
/// own app. Run with `make -C mac uitest DESKPIXIE_UITEST_SESSIONS=1`. The sessions use your
/// claude login (the real home); a message sent to one starts the coordinator, so those tests
/// cost a little: their prompts are one word.
@MainActor
final class ChatSessionsUITests: XCTestCase {
    private var harness: UITestApp?
    private var sessions: TmuxSessions?
    private var pasteboard: SavedPasteboard?

    override func setUp() async throws {
        continueAfterFailure = false
        try XCTSkipUnless(UITestApp.withSessions, "set DESKPIXIE_UITEST_SESSIONS=1 to run the connected-sessions tests")
        try XCTSkipUnless(TmuxSessions.tmux != nil, "no tmux")
        pasteboard = SavedPasteboard()
    }

    override func tearDown() async throws {
        sessions?.cleanUp()
        sessions = nil
        if let harness {
            XCTAssertTrue(harness.cleanUp(),
                          "~/Library/Preferences/\(UITestApp.userBundleID).plist changed during the test")
        }
        harness = nil
        pasteboard?.restore()
        pasteboard = nil
    }

    /// The app (agent on, warm off: no coordinator until a message is sent), alpha and beta
    /// running in it, and the window open with both in the @ list.
    private func start() throws -> (UITestApp, XCUIElement, XCUIElement) {
        let h = try UITestApp(agent: true, realHome: true)
        harness = h
        let s = try TmuxSessions(root: h.root, support: h.support)
        sessions = s
        h.launch()
        try s.start("alpha")
        try s.start("beta")
        let window = h.openChat()
        let composer = window.element("chat.composer")
        XCTAssertTrue(composer.waitForExistence(timeout: 10))
        composer.click()
        for name in ["alpha", "beta"] {
            XCTAssertTrue(waitForRecipient(name, in: window, composer),
                          "\(name) never came to the list; its pane:\n\(s.capture(name))")
        }
        return (h, window, composer)
    }

    private func rows(_ window: XCUIElement) -> XCUIElementQuery {
        window.descendants(matching: .any).matching(identifier: "chat.mention.row")
    }

    /// Types `@` until the list shows this session (the roster lists it once claude is up), then
    /// takes the @ out again.
    private func waitForRecipient(_ label: String, in window: XCUIElement, _ composer: XCUIElement) -> Bool {
        let deadline = Date().addingTimeInterval(90)
        while Date() < deadline {
            composer.typeText("@")
            let row = rows(window).matching(NSPredicate(format: "label BEGINSWITH %@", label + ",")).firstMatch
            let there = row.waitForExistence(timeout: 3)
            composer.typeKey(.delete, modifierFlags: [])
            if there { return true }
            Thread.sleep(forTimeInterval: 2)
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

    // MARK: Sending (starts the coordinator)

    /// Sent to alpha, the message lands in alpha's pane and is submitted: claude answers it. Its
    /// answer comes back to the thread with the terminal button, which reaches alpha's session.
    func testASendToAlphaLandsInItsPaneAndItsTerminalButtonReachesIt() throws {
        let (h, window, composer) = try start()
        pick("alp", composer)
        XCTAssertTrue(window.element("chat.replyChip").waitForText("Replying to alpha", timeout: 5))
        let words = "reply with ok"
        composer.typeText(words)
        composer.typeKey(.return, modifierFlags: [])

        let s = try XCTUnwrap(sessions)
        XCTAssertTrue(s.waitForSubmitted(words, in: "alpha", timeout: 300),
                      "not submitted in alpha's pane:\n\(s.capture("alpha"))")
        XCTAssertFalse(s.capture("beta").contains(words), "beta got it too")

        // alpha's answer, as an entry of its own with the terminal button by its name.
        let button = window.buttons.matching(identifier: "chat.entry.terminal")
            .matching(NSPredicate(format: "label == %@", "Open alpha's terminal")).firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 120), "no message from alpha in the thread")
        let before = statusLines(h)
        button.click()
        // alpha runs in a detached tmux server, with no terminal app to bring forward: the core
        // finds the session and says so in the menu. A wrong address would say "no session of
        // yours named …".
        let deadline = Date().addingTimeInterval(15)
        var lines = before
        repeat {
            Thread.sleep(forTimeInterval: 1)
            lines = statusLines(h)
        } while !lines.contains(where: { $0.contains("could not find the terminal of session process") }) && Date() < deadline
        XCTAssertTrue(lines.contains { $0.contains("could not find the terminal of session process") },
                      "focusSession didn't reach alpha: \(lines)")
        XCTAssertFalse(lines.contains { $0.contains("no session of yours named") }, "\(lines)")
    }

    /// The same with an image attached: it only submits once #305 (branch fix-attach-enter) is in.
    func testASendWithAnImageToAlphaIsSubmitted() throws {
        let (_, window, composer) = try start()
        pick("alp", composer)
        XCTAssertTrue(window.element("chat.replyChip").waitForText("Replying to alpha", timeout: 5))
        let pb = NSPasteboard.general
        pb.clearContents()
        pb.setData(Self.png(), forType: .png)
        composer.typeKey("v", modifierFlags: .command)
        XCTAssertTrue(window.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Remove '")).firstMatch
            .waitForExistence(timeout: 10), "the image didn't attach")
        let words = "reply with ok"
        composer.typeText(words)
        composer.typeKey(.return, modifierFlags: [])
        let s = try XCTUnwrap(sessions)
        XCTExpectFailure("#305: a message with an attachment isn't submitted until fix-attach-enter is merged")
        XCTAssertTrue(s.waitForSubmitted(words, in: "alpha", timeout: 240),
                      "not submitted in alpha's pane:\n\(s.capture("alpha"))")
    }

    // MARK: A session that ends

    func testBetaEndingWhilePickedSaysSo() throws {
        let (_, window, composer) = try start()
        pick("bet", composer)
        XCTAssertTrue(window.element("chat.replyChip").waitForText("Replying to beta", timeout: 5))
        try XCTUnwrap(sessions).stop("beta")
        composer.typeText("reply with ok")
        XCTAssertTrue(window.waitForText("beta isn't running anymore.", timeout: 60), "no notice once beta ended")
        composer.typeKey(.return, modifierFlags: [])
        XCTAssertEqual(draft(composer), "reply with ok", "nothing went; the draft stays")
        XCTAssertTrue(window.waitForText("beta isn't running anymore.", timeout: 5))
    }

    // MARK: Helpers

    /// The menu bar menu's status lines (the labels above its first separator).
    private func statusLines(_ h: UITestApp) -> [String] {
        let status = h.app.statusItems.firstMatch
        status.click()
        let items = h.app.menuItems.allElementsBoundByIndex.map(\.title)
        h.app.typeKey(.escape, modifierFlags: [])
        return items.filter { $0.hasPrefix("Coordinator:") }
    }

    private static func png() -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 16, pixelsHigh: 16, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        for x in 0..<16 { for y in 0..<16 { rep.setColor(.systemGreen, atX: x, y: y) } }
        return rep.representation(using: .png, properties: [:])!
    }
}

/// Sessions of yours for a test: `pix --description <name> claude --permission-mode default` in
/// panes of a private tmux server (`tmux -L dpui-<run>`, no config), each in its own folder under
/// the run's root, connected to the test's app by its data folder. claude trusts those folders
/// through its own `~/.claude.json` (`projects.<path>.hasTrustDialogAccepted`): only entries this
/// run added, which `cleanUp` takes out again.
@MainActor
final class TmuxSessions {
    static let tmux: String? = ["/opt/homebrew/bin/tmux", "/usr/local/bin/tmux", "/usr/bin/tmux"]
        .first { FileManager.default.isExecutableFile(atPath: $0) }

    private let server: String
    private let root: URL
    private let support: URL
    private var folders: [URL] = []
    private var trusted: [String] = []

    init(root: URL, support: URL) throws {
        self.root = root.appendingPathComponent("sessions", isDirectory: true)
        self.support = support
        server = "dpui-" + root.lastPathComponent.replacingOccurrences(of: "dpui-", with: "")
        try FileManager.default.createDirectory(at: self.root, withIntermediateDirectories: true)
    }

    /// tmux with a clean environment, as a login would give it: the test runner's own (Xcode's
    /// DYLD_* paths, its bundle id, TERM=dumb) would reach claude, which then never draws.
    private func tmux(_ args: [String]) -> (status: Int32, out: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: Self.tmux!)
        p.arguments = ["-L", server, "-f", "/dev/null"] + args
        let env = ProcessInfo.processInfo.environment
        var clean = ["HOME": Self.home, "PATH": Self.path, "LANG": "en_US.UTF-8", "SHELL": "/bin/zsh"]
        for key in ["USER", "LOGNAME", "TMPDIR", "SSH_AUTH_SOCK"] { clean[key] = env[key] }
        p.environment = clean
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return (-1, "") }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, String(decoding: data, as: UTF8.self))
    }

    private static var home: String { claudeJSON.deletingLastPathComponent().path }
    /// The test runner's PATH is short: claude is where its installer puts it, node in Homebrew's.
    private static var path: String {
        ["\(home)/.local/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"].joined(separator: ":")
    }

    func start(_ name: String) throws {
        let folder = root.appendingPathComponent(name, isDirectory: true)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        folders.append(folder)
        try trust(folder)
        let pix = UITestApp.builtApp.appendingPathComponent("Contents/Helpers/pix").path
        // The parent's pix and tmux variables would report to another pix, or nest (the clean
        // server has none; this says so where it matters).
        let command = ["env", "-u", "PIX_SOCK", "-u", "PIX_SESSION", "-u", "DESKPIXIE_PIX_SOCK", "-u", "TMUX",
                       "DESKPIXIE_SUPPORT_DIR=\(support.path)", pix, "--description", name, "claude",
                       "--permission-mode", "default"].map(Self.quote).joined(separator: " ")
        let made = tmux(["new-session", "-d", "-s", name, "-x", "200", "-y", "50", "-c", folder.path, command])
        XCTAssertEqual(made.status, 0, "tmux new-session \(name) failed")
    }

    /// Ends one session: its pane goes, and pix with it.
    func stop(_ name: String) {
        tmux(["kill-session", "-t", name])
    }

    func capture(_ name: String) -> String {
        tmux(["capture-pane", "-p", "-J", "-t", name, "-S", "-300"]).out
    }

    /// The words were submitted in this pane: they are there, and claude has answered below
    /// them (its ⏺), so they don't just sit in the prompt line.
    func waitForSubmitted(_ words: String, in name: String, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            let lines = capture(name).components(separatedBy: "\n")
            if let i = lines.lastIndex(where: { $0.contains(words) }),
               lines[(i + 1)...].contains(where: { $0.trimmingCharacters(in: .whitespaces).hasPrefix("⏺") }) {
                return true
            }
            Thread.sleep(forTimeInterval: 2)
        } while Date() < deadline
        return false
    }

    /// The tmux server, the folders, claude's trust entries for them, and those the app made
    /// under this run's folder (the coordinator's workspace).
    func cleanUp() {
        tmux(["kill-server"])
        untrust()
        let run = root.deletingLastPathComponent()
        let real = realpath(run.path, nil).map { p in defer { free(p) }; return String(cString: p) } ?? run.path
        try? editClaudeJSON { projects in
            for key in projects.keys where key.hasPrefix(run.path + "/") || key.hasPrefix(real + "/") {
                projects.removeValue(forKey: key)
            }
        }
        for f in folders.reversed() { try? FileManager.default.removeItem(at: f) }
        try? FileManager.default.removeItem(at: root)
    }

    // MARK: claude's trust

    private static var claudeJSON: URL {
        let home = getpwuid(getuid()).map { String(cString: $0.pointee.pw_dir) } ?? NSHomeDirectory()
        return URL(fileURLWithPath: home).appendingPathComponent(".claude.json")
    }

    /// As claude records "Yes, I trust this folder", for the path as given and as resolved
    /// (/tmp is /private/tmp, which claude names it by).
    private func trust(_ folder: URL) throws {
        // realpath, not resolvingSymlinksInPath: Foundation drops /private again.
        let real = realpath(folder.path, nil).map { p in defer { free(p) }; return String(cString: p) } ?? folder.path
        let paths = Set([folder.path, real])
        try editClaudeJSON { projects in
            for p in paths where projects[p] == nil {
                projects[p] = ["hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true]
                trusted.append(p)
            }
        }
    }

    private func untrust() {
        guard !trusted.isEmpty else { return }
        try? editClaudeJSON { projects in
            for p in trusted { projects.removeValue(forKey: p) }
        }
        trusted = []
    }

    /// Reads ~/.claude.json, changes its projects, writes it back in one go (only when entries
    /// came or went: claude itself writes this file too).
    private func editClaudeJSON(_ change: (inout [String: Any]) -> Void) throws {
        let url = Self.claudeJSON
        let data = (try? Data(contentsOf: url)) ?? Data("{}".utf8)
        guard var top = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw XCTSkip("~/.claude.json isn't a JSON object")
        }
        var projects = top["projects"] as? [String: Any] ?? [:]
        let keys = Set(projects.keys)
        change(&projects)
        guard Set(projects.keys) != keys else { return } // nothing to write
        top["projects"] = projects
        try JSONSerialization.data(withJSONObject: top, options: [.prettyPrinted, .withoutEscapingSlashes]).write(to: url, options: .atomic)
    }

    private static func quote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }
}
