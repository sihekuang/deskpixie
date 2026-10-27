import AppKit
import XCTest

/// One DeskPixie for a UI test, isolated from the user's own (AGENTS.md). It is the UITest
/// build, under its own bundle ID, so its preferences, window frames and Keychain items are its
/// own and the user's DeskPixie can stay open. Its data folder (`DESKPIXIE_SUPPORT_DIR`) is a
/// fresh folder with its own settings.json, its own `agent.session` and a free `agent.port`, and
/// offline runs get a throwaway home too. Nothing in the app knows it is under test: the folder
/// is set up the way the app itself would leave it.
@MainActor
final class UITestApp {
    /// The UITest configuration's PRODUCT_BUNDLE_IDENTIFIER (mac/project.yml).
    static let bundleID = "com.sihekuang.deskpixie.uitest"
    /// The user's own app: its preferences must not change while the tests run.
    static let userBundleID = "com.sihekuang.deskpixie"
    /// `DESKPIXIE_UITEST_LIVE=1` (passed as `TEST_RUNNER_DESKPIXIE_UITEST_LIVE` by `make uitest`):
    /// the tests that need herdr, the coordinator and a model run too.
    static var live: Bool { ProcessInfo.processInfo.environment["DESKPIXIE_UITEST_LIVE"] == "1" }
    /// `DESKPIXIE_UITEST_SESSIONS=1` (`TEST_RUNNER_DESKPIXIE_UITEST_SESSIONS`): the tests with
    /// sessions of yours connected to the app, stand-ins for claude (`ChatSessionsUITests`,
    /// `StopAgentUITests`).
    static var withSessions: Bool { ProcessInfo.processInfo.environment["DESKPIXIE_UITEST_SESSIONS"] == "1" }

    /// A short path under /tmp: the core keeps its pix socket under the data folder, and a long
    /// one would fall back to a socket shared with the user's app.
    let root: URL
    let support: URL
    let home: URL
    /// `dp-uitest-<hex>`: a herdr session name (a-z, 0-9, -).
    let session: String
    let port: Int
    let live: Bool
    /// The real home, for claude's login: live runs.
    let realHome: Bool
    /// `agent.enabled`. Attachments live in the agent, so the clipboard tests turn it on: with
    /// `warm` off it starts no coordinator until a message is sent.
    let agent: Bool
    let app = XCUIApplication()
    private var launched = false
    /// The user's preferences file as it was before this test (nil: not there).
    private let userPreferencesDate = UITestApp.userPreferencesModified()

    /// Skips the test while a copy of the UITest build it didn't launch is running (launching
    /// here would end it). The user's own DeskPixie, under its real bundle ID, doesn't count.
    /// `settings`: top-level settings.json values this test sets on top of the ones every test has.
    init(live: Bool = false, agent: Bool? = nil, settings extra: [String: Any] = [:]) throws {
        if !NSRunningApplication.runningApplications(withBundleIdentifier: Self.bundleID).isEmpty {
            throw XCTSkip("Quit DeskPixie UITest to run the UI tests")
        }
        let tag = UUID().uuidString.replacingOccurrences(of: "-", with: "").lowercased().prefix(12)
        self.live = live
        self.agent = agent ?? live
        self.realHome = live
        root = URL(fileURLWithPath: "/tmp/dpui-\(tag)", isDirectory: true)
        support = root.appendingPathComponent("support", isDirectory: true)
        home = root.appendingPathComponent("home", isDirectory: true)
        session = "dp-uitest-\(tag)"
        port = try Self.freePort()
        let fm = FileManager.default
        try fm.createDirectory(at: support.appendingPathComponent("chat"), withIntermediateDirectories: true)
        try fm.createDirectory(at: home, withIntermediateDirectories: true)
        try fm.createDirectory(at: root.appendingPathComponent("projects"), withIntermediateDirectories: true)
        try writeSettings(extra)
    }

    // MARK: The data folder

    /// settings.json as the core writes it, with this run's own session and port. Absent keys
    /// get the core's defaults (`settings.ParseConfig`). A file that is already there is what
    /// marks onboarding done for a data folder (`OnboardingGate.shouldOpenAtLaunch`).
    private func writeSettings(_ extra: [String: Any]) throws {
        var settings: [String: Any] = [
            "chatKeepDays": 7,
            // Not the default ctrl+alt+Space, which the user's DeskPixie holds; one that can't be
            // bound only shows a line in the menu.
            "hotkey": "ctrl+alt+shift+cmd+F19",
            "canvas": ["autoOpen": false, "keep": 50],
            "agent": [
                // Off, no coordinator: no herdr session, no hook server, no model.
                "enabled": agent,
                "session": session,
                "port": port,
                "keepPanesOnQuit": false,
                "warm": false,
                "projectsRoot": root.appendingPathComponent("projects").path,
            ],
        ]
        settings.merge(extra) { _, new in new }
        let data = try JSONSerialization.data(withJSONObject: settings, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: support.appendingPathComponent("settings.json"))
    }

    /// `chat/thread.jsonl` in the store's own format (`core/chat`): one entry's JSON a line, in
    /// time order, which the store keeps.
    func seed(_ entries: [SeedEntry]) throws {
        let encoder = JSONEncoder()
        var text = Data()
        for e in entries {
            text.append(try encoder.encode(e))
            text.append(0x0A)
        }
        try text.write(to: support.appendingPathComponent("chat/thread.jsonl"))
    }

    // MARK: Sessions of yours

    /// A running session: `script`'s pid, and the input it reads, held open.
    private struct Session { let pid: pid_t; let input: Int32 }
    private var sessions: [String: Session] = [:]

    /// A session of yours, as `pix --description <description> claude` in a terminal would start
    /// one: the app's own pix, in a pty (`script`), on this run's data folder and home, with a
    /// stand-in claude that does nothing but start and end the turns `startTurn` and `endTurn`
    /// give it, and take an Esc as claude does (`escaped`). Nothing reaches the user's own pix:
    /// no PIX_* from this process, and the socket is this data folder's. It runs apart from this
    /// test runner (its parent is launchd), as in a terminal the app can't name: the terminal
    /// button then says "could not find the terminal of session process …" rather than bring
    /// the runner, which won't come forward, to the front.
    func startSession(description: String) throws {
        let bin = root.appendingPathComponent("bin", isDirectory: true)
        let claude = bin.appendingPathComponent("claude")
        let fm = FileManager.default
        let pix = Self.builtApp.appendingPathComponent("Contents/Helpers/pix").path
        if !fm.fileExists(atPath: claude.path) {
            try fm.createDirectory(at: bin, withIntermediateDirectories: true)
            // A turn starts and ends as claude's hooks do: each JSON file put in $TURNS goes to
            // `pix hook <event>` (what pix's settings tell claude to run), the event named by the
            // file's name up to its first "-". A lone Esc typed into it (pix interrupting it) is
            // noted in $TURNS.esc and answered with what claude shows then (its interrupt line over
            // the input box, #322), which ends the turn for pix; an Esc that a "[" or "O" follows
            // within 0.1 s starts an escape sequence (a bracketed paste, a key), not an interrupt.
            // Every other key typed into it is noted in $TURNS.keys (in hex), and draws the next
            // screen waiting in $TURNS.screens when that one is for this key (`askMenu`); a
            // .screen file put in $TURNS is drawn as it comes. It stops with its pix.
            let script = """
            #!/bin/sh
            stty -icanon -echo min 1 time 0 2>/dev/null
            (
              while kill -0 $PPID 2>/dev/null; do
                c=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' \\n')
                [ -n "$c" ] || sleep 0.2
                if [ "$c" = "1b" ]; then
                  stty min 0 time 1 2>/dev/null
                  n=$(dd bs=1 count=1 2>/dev/null | od -An -tx1 | tr -d ' \\n')
                  stty min 1 time 0 2>/dev/null
                  if [ "$n" != "5b" ] && [ "$n" != "4f" ]; then
                    touch "$TURNS.esc"
                    printf '  ⎿  Interrupted · What should Claude do instead?\\r\\n────\\r\\n❯ \\r\\n────\\r\\n'
                  fi
                elif [ -n "$c" ]; then
                  printf '%s ' "$c" >> "$TURNS.keys"
                  for s in "$TURNS".screens/*; do
                    [ -e "$s" ] || break
                    case "$s" in *-"$c") cat "$s"; rm -f "$s";; esac
                    break
                  done
                fi
              done
            ) <&0 & # a job in the background reads /dev/null unless told otherwise
            while kill -0 $PPID 2>/dev/null; do
              for f in "$TURNS"/*.screen; do
                [ -e "$f" ] || continue
                cat "$f"
                rm -f "$f"
              done
              for f in "$TURNS"/*.json; do
                [ -e "$f" ] || continue
                ev=$(basename "$f")
                "\(pix)" hook "${ev%%-*}" < "$f"
                rm -f "$f"
              done
              sleep 0.3
            done
            """
            try script.write(to: claude, atomically: true, encoding: .utf8)
            try fm.setAttributes([.posixPermissions: 0o755], ofItemAtPath: claude.path)
        }
        let turns = root.appendingPathComponent("turns-\(description)", isDirectory: true)
        try fm.createDirectory(at: turns, withIntermediateDirectories: true)
        // script's input is a pipe from cat, which reads a FIFO this process holds open: it never
        // ends, so the session keeps running. (script refuses a FIFO itself as its input.)
        let fifo = root.appendingPathComponent("input-\(description)").path
        guard mkfifo(fifo, 0o600) == 0 else { throw SessionError(description: "mkfifo \(fifo): \(errno)") }
        let input = open(fifo, O_RDWR)
        guard input >= 0 else { throw SessionError(description: "open \(fifo): \(errno)") }
        // sh starts script in the background and leaves: script is launchd's from then on.
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/sh")
        p.arguments = ["-c", #"cat "$2" 2>/dev/null | /usr/bin/script -q /dev/null "$0" --description "$1" claude > /dev/null 2>&1 & echo $!"#,
                       pix, description, fifo]
        p.currentDirectoryURL = root.appendingPathComponent("projects")
        p.environment = ["HOME": home.path, "DESKPIXIE_SUPPORT_DIR": support.path, "TERM": "xterm-256color",
                         "PATH": "\(bin.path):/usr/bin:/bin", "TURNS": turns.path]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        try p.run()
        let said = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        p.waitUntilExit()
        guard let pid = pid_t(said.trimmingCharacters(in: .whitespacesAndNewlines)), pid > 1 else {
            close(input)
            throw SessionError(description: "no pid for \(description): \(said)")
        }
        sessions[description] = Session(pid: pid, input: input)
    }

    struct SessionError: Error, CustomStringConvertible { let description: String }

    /// Ends the session `description`, as closing its terminal would: pix goes, and the app
    /// stops counting it.
    func stopSession(_ description: String) {
        guard let s = sessions.removeValue(forKey: description) else { return }
        Self.end(s)
    }

    /// Freezes (SIGSTOP) or thaws (SIGCONT) the session `description`'s pix: frozen, it stays
    /// listed as it was, but answers nothing. The pid of the pix it froze; nil if none was found.
    @discardableResult
    func signalPix(_ description: String, _ signal: Int32) -> pid_t? {
        guard let s = sessions[description] else { return nil }
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/pgrep")
        p.arguments = ["-P", String(s.pid), "-x", "pix"]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return nil }
        let said = String(decoding: out.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        p.waitUntilExit()
        guard let pid = pid_t(said.split(separator: "\n").first ?? ""), pid > 1 else { return nil }
        kill(pid, signal)
        return pid
    }

    /// SIGTERM to script, then waits up to 10 s for it to go (launchd reaps it).
    private static func end(_ s: Session) {
        kill(s.pid, SIGTERM)
        let deadline = Date().addingTimeInterval(10)
        while kill(s.pid, 0) == 0 && Date() < deadline { Thread.sleep(forTimeInterval: 0.1) }
        close(s.input)
    }

    /// The session `description` finishes a turn with this reply, as claude's Stop hook would
    /// report it to pix.
    func endTurn(_ description: String, reply: String) throws {
        try hook(description, ["hook_event_name": "Stop", "last_assistant_message": reply])
    }

    /// The session `description` takes this prompt and works on it, as claude's
    /// UserPromptSubmit hook would report it to pix, until `endTurn` or an Esc.
    func startTurn(_ description: String, prompt: String) throws {
        try hook(description, ["hook_event_name": "UserPromptSubmit", "prompt": prompt])
    }

    /// The session `description` was sent an Esc since it started.
    func escaped(_ description: String) -> Bool {
        FileManager.default.fileExists(atPath: root.appendingPathComponent("turns-\(description).esc").path)
    }

    /// The session `description` asks Claude's menu tool with `input`, its menu drawn as
    /// `screen`; then each key typed into it, as `after` lists them in turn, draws the screen
    /// beside it, as claude's own menu does (#364).
    func askMenu(_ description: String, input: [String: Any], screen: String, after: [(key: Character, screen: String)]) throws {
        let turns = root.appendingPathComponent("turns-\(description)", isDirectory: true)
        let screens = root.appendingPathComponent("turns-\(description).screens", isDirectory: true)
        try FileManager.default.createDirectory(at: screens, withIntermediateDirectories: true)
        for (i, a) in after.enumerated() {
            let hex = a.key.asciiValue.map { String(format: "%02x", $0) } ?? ""
            try Data(("\u{1b}[2J\u{1b}[H" + a.screen).utf8).write(to: screens.appendingPathComponent(String(format: "%02d-%@", i, hex)))
        }
        let tmp = turns.appendingPathComponent(".draw")
        try Data(("\u{1b}[2J\u{1b}[H" + screen).utf8).write(to: tmp)
        try FileManager.default.moveItem(at: tmp, to: turns.appendingPathComponent("menu-\(UUID().uuidString).screen"))
        try hook(description, ["hook_event_name": "PreToolUse", "tool_name": "AskUserQuestion", "tool_input": input])
    }

    /// The keys typed into the session `description` since it started, but Esc.
    func keysTyped(_ description: String) -> String {
        let file = root.appendingPathComponent("turns-\(description).keys")
        let hex = (try? String(contentsOf: file, encoding: .utf8)) ?? ""
        return String(decoding: hex.split(separator: " ").compactMap { UInt8($0, radix: 16) }, as: UTF8.self)
    }

    private func hook(_ description: String, _ payload: [String: Any]) throws {
        let turns = root.appendingPathComponent("turns-\(description)", isDirectory: true)
        let data = try JSONSerialization.data(withJSONObject: payload)
        let tmp = turns.appendingPathComponent(".turn")
        try data.write(to: tmp)
        let event = payload["hook_event_name"] as? String ?? "Stop"
        try FileManager.default.moveItem(at: tmp, to: turns.appendingPathComponent("\(event)-\(UUID().uuidString).json"))
    }

    // MARK: The app

    /// Launches the built app on this folder. Onboarding's keys come in the argument domain,
    /// which is read before the app's own defaults and never written back.
    func launch() {
        var env = ["DESKPIXIE_SUPPORT_DIR": support.path]
        if !realHome {
            // Offline, the core's home and the app's preferences are this run's own too. Live
            // runs keep the real home: the coordinator and claude need its login.
            env["HOME"] = home.path
            env["CFFIXED_USER_HOME"] = home.path
        }
        app.launchEnvironment = env
        app.launchArguments = [
            "-DeskPixieOnboardingDone", "YES",
            "-DeskPixiePermissionsCheckedVersion", Self.appVersion,
            "-ApplePersistenceIgnoreState", "YES",
        ]
        app.launch()
        launched = true
    }

    func terminate() {
        guard launched else { return }
        app.terminate()
        launched = false
    }

    /// Ends the app, the herdr session it may have made, and the folder. False when the user's
    /// own preferences file changed meanwhile.
    @discardableResult
    func cleanUp() -> Bool {
        for s in sessions.values { Self.end(s) }
        sessions = [:]
        terminate()
        if let herdr = Self.herdr {
            _ = Self.run(herdr, ["session", "stop", session])
            _ = Self.run(herdr, ["session", "delete", session])
        }
        try? FileManager.default.removeItem(at: root)
        return Self.userPreferencesModified() == userPreferencesDate
    }

    /// ~/Library/Preferences/com.sihekuang.deskpixie.plist in the real home (the account's, not
    /// $HOME): its modification date, nil when it isn't there.
    private static func userPreferencesModified() -> Date? {
        guard let pw = getpwuid(getuid()), let dir = pw.pointee.pw_dir else { return nil }
        let file = URL(fileURLWithPath: String(cString: dir))
            .appendingPathComponent("Library/Preferences/\(userBundleID).plist")
        let attributes = try? FileManager.default.attributesOfItem(atPath: file.path)
        return attributes?[.modificationDate] as? Date
    }

    /// The chat window, from the round button above Pixie: always on screen, where a menu bar
    /// item can sit past the edge of a crowded menu bar and never become clickable. The menu
    /// bar's Chat… has its own test (`openChatFromMenuBar`).
    @discardableResult
    func openChat(file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        let button = app.buttons["pixie.chatButton"]
        XCTAssertTrue(button.waitForExistence(timeout: 60), "no chat button above Pixie", file: file, line: line)
        let window = chatWindow
        if !window.exists { button.click() }
        XCTAssertTrue(window.waitForExistence(timeout: 15), "the chat window didn't open", file: file, line: line)
        return window
    }

    /// The menu bar's Chat…, then the window.
    @discardableResult
    func openChatFromMenuBar(file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        chooseFromMenuBar("Chat…", typing: "chat", file: file, line: line)
        let window = chatWindow
        XCTAssertTrue(window.waitForExistence(timeout: 15), "the chat window didn't open", file: file, line: line)
        return window
    }

    /// Chooses item in the menu bar's menu by typing its first letters. Items like Chat… and
    /// Settings… wait for the core to start, so the menu is opened again until it can be chosen.
    func chooseFromMenuBar(_ item: String, typing: String, file: StaticString = #filePath, line: UInt = #line) {
        let status = app.statusItems.firstMatch
        XCTAssertTrue(status.waitForExistence(timeout: 30), "no menu bar item", file: file, line: line)
        // Just after a launch the item may be listed before it has its place in the menu bar, and a
        // click on it then opens nothing and fails outright ("menu open notification").
        let placed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "hittable == true"), object: status)
        XCTAssertEqual(XCTWaiter().wait(for: [placed], timeout: 30), .completed, "the menu bar item never took its place",
                       file: file, line: line)
        let deadline = Date().addingTimeInterval(60)
        while Date() < deadline {
            status.click()
            // The menu bar's own, not the app menu's (Settings… is in both).
            let entry = status.menuItems[item]
            if entry.waitForExistence(timeout: 5), entry.isEnabled {
                // Chosen by typing its name and Return, not by clicking the item: while the core
                // starts, the menu is rebuilt as it stays open (status lines, the agent), and a
                // click on an item that was just replaced fails the test outright ("open menu
                // during menu traversal"). The menu's own type-select finds the new one.
                for key in typing { app.typeKey(String(key), modifierFlags: []) }
                app.typeKey(.return, modifierFlags: [])
                return
            }
            app.typeKey(.escape, modifierFlags: [])
            Thread.sleep(forTimeInterval: 1) // the core is still starting: ask again in a moment
        }
        XCTFail("\(item) never could be chosen from the menu bar", file: file, line: line)
    }

    var chatWindow: XCUIElement { app.windows["chat.window"] }

    // MARK: Helpers

    /// "0.3.0 (1)", as the app records the version it checked permissions for: the test bundle
    /// is built with the app's MARKETING_VERSION and CURRENT_PROJECT_VERSION.
    static var appVersion: String {
        let info = Bundle(for: UITestApp.self).infoDictionary
        return "\(info?["CFBundleShortVersionString"] as? String ?? "?") (\(info?["CFBundleVersion"] as? String ?? "?"))"
    }

    /// The app this test bundle was built with: Debug/DeskPixieUITests-Runner.app/Contents/
    /// PlugIns/DeskPixieUITests.xctest, next to Debug/DeskPixie.app.
    static var builtApp: URL {
        var url = Bundle(for: UITestApp.self).bundleURL
        for _ in 0..<4 { url.deleteLastPathComponent() }
        return url.appendingPathComponent("DeskPixie.app")
    }

    static var herdr: String? {
        ["/opt/homebrew/bin/herdr", "/usr/local/bin/herdr", NSHomeDirectory() + "/.local/bin/herdr"]
            .first { FileManager.default.isExecutableFile(atPath: $0) }
    }

    /// A port nothing listens on now: bind 0, read it back, close.
    private static func freePort() throws -> Int {
        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { throw XCTSkip("no socket for a free port") }
        defer { close(fd) }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = inet_addr("127.0.0.1")
        addr.sin_port = 0
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let bound = withUnsafeMutablePointer(to: &addr) { p in
            p.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                bind(fd, sa, len) == 0 && getsockname(fd, sa, &len) == 0
            }
        }
        guard bound else { throw XCTSkip("couldn't find a free port") }
        return Int(UInt16(bigEndian: addr.sin_port))
    }

    /// Runs a program to its end: its exit status and what it printed.
    @discardableResult
    static func run(_ path: String, _ args: [String], env: [String: String] = [:]) -> (status: Int32, out: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: path)
        p.arguments = args
        p.environment = ProcessInfo.processInfo.environment.merging(env) { _, new in new }
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.nullDevice
        do { try p.run() } catch { return (-1, "") }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, String(decoding: data, as: UTF8.self))
    }
}

/// One `core/chat` Entry as thread.jsonl holds it (`at` in whole milliseconds).
struct SeedEntry: Encodable {
    struct Who: Encodable {
        var kind: String
        var name: String?
        var label: String?
        var yours: Bool?
        var status: String?
        var color = 0
    }

    struct Request: Encodable {
        var token: String?
        var item: UInt64
        var kind: String
        var options: [String]?
        var typed: Bool?
        var outcome: String?
    }

    var id: String
    var at: Int64
    var who: Who
    var text: String
    var sessions: [String]?
    var request: Request?
    var read: Bool? = true

    /// Milliseconds since the epoch, `ago` seconds back.
    static func ms(ago seconds: TimeInterval) -> Int64 {
        Int64((Date().timeIntervalSince1970 - seconds) * 1000)
    }
}

/// The general pasteboard's items, copied out, to put back when a test is done with it.
@MainActor
struct SavedPasteboard {
    private let items: [[NSPasteboard.PasteboardType: Data]]

    init() {
        items = (NSPasteboard.general.pasteboardItems ?? []).map { item in
            var copy: [NSPasteboard.PasteboardType: Data] = [:]
            for type in item.types {
                if let data = item.data(forType: type) { copy[type] = data }
            }
            return copy
        }
    }

    func restore() {
        let pb = NSPasteboard.general
        pb.clearContents()
        let restored = items.map { copy in
            let item = NSPasteboardItem()
            for (type, data) in copy { item.setData(data, forType: type) }
            return item
        }
        if !restored.isEmpty { pb.writeObjects(restored) }
    }
}

// MARK: Queries

extension XCUIElement {
    /// Any element under this one with this accessibility identifier.
    func element(_ id: String) -> XCUIElement {
        descendants(matching: .any).matching(identifier: id).firstMatch
    }

    /// Words shown under this element: a label, or a text's value (a message's text view). The
    /// kind of element is picked when this is called, and the last resort matches labels only,
    /// so words that may not be there yet are waited for with `waitForText`, not on this.
    func text(_ words: String) -> XCUIElement {
        let label = NSPredicate(format: "label CONTAINS %@", words)
        let value = NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", words, words)
        let texts = staticTexts.matching(value)
        if texts.firstMatch.exists { return texts.firstMatch }
        let views = textViews.matching(value)
        if views.firstMatch.exists { return views.firstMatch }
        return descendants(matching: .any).matching(label).firstMatch
    }

    /// Waits for these words to show under this element.
    func waitForText(_ words: String, timeout: TimeInterval) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        repeat {
            if text(words).exists { return true }
            Thread.sleep(forTimeInterval: 0.25)
        } while Date() < deadline
        return false
    }

    /// Waits for this element to go.
    func waitForGone(timeout: TimeInterval) -> Bool {
        let gone = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: self)
        return XCTWaiter().wait(for: [gone], timeout: timeout) == .completed
    }
}
