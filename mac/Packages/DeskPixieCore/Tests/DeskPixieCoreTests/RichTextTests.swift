import AppKit
import XCTest
@testable import DeskPixieCore

final class RichTextTests: XCTestCase {
    private func span(_ kind: String, _ text: String, raw: String? = nil, bold: Bool = false, url: String? = nil,
                      path: String? = nil, dir: Bool = false, line: Int = 0, open: [String] = []) -> RichSpan {
        RichSpan(kind: kind, text: text, raw: raw ?? text, bold: bold, url: url, path: path, dir: dir, line: line,
                 col: 0, open: open)
    }

    func testDecodesTheCoresSpans() throws {
        let json = #"{"spans":[{"kind":"text","text":"see ","raw":"see "},{"kind":"path","text":"a.go:3","raw":"a.go:3","path":"/w/a.go","line":3,"open":["vscode://file/w/a.go:3","file:///w/a.go"]},{"kind":"text","text":"Done","raw":"**Done**","bold":true}]}"#
        let spans = try JSONDecoder().decode(RichSpans.self, from: Data(json.utf8)).spans
        XCTAssertEqual(spans.count, 3)
        XCTAssertEqual(spans[1].path, "/w/a.go")
        XCTAssertEqual(spans[1].line, 3)
        XCTAssertEqual(spans[1].open.count, 2)
        XCTAssertFalse(spans[1].dir)
        XCTAssertTrue(spans[2].bold)
        XCTAssertFalse(spans[0].isActionable)
        XCTAssertTrue(spans[1].isActionable)
    }

    // What a hover says, and what a click does, for each kind.
    func testTooltipsAndActions() {
        let link = span("link", "docs", raw: "[docs](https://x.com/d)", url: "https://x.com/d")
        XCTAssertEqual(link.tooltip(copied: false), "Open link\nhttps://x.com/d")
        XCTAssertEqual(link.action, .open([URL(string: "https://x.com/d")!]))

        let file = span("path", "a.go:3", path: "/w/a.go", line: 3, open: ["vscode://file/w/a.go:3", "file:///w/a.go"])
        XCTAssertEqual(file.tooltip(copied: false), "Open in your editor · line 3\n⌥-click: show in Finder")
        XCTAssertEqual(file.action, .open([URL(string: "vscode://file/w/a.go:3")!, URL(string: "file:///w/a.go")!]))
        let plainFile = span("path", "a.go", path: "/w/a.go", open: ["file:///w/a.go"])
        XCTAssertEqual(plainFile.tooltip(copied: false), "Open file\n⌥-click: show in Finder")
        let folder = span("path", "docs/", path: "/w/docs", dir: true, open: ["file:///w/docs"])
        XCTAssertEqual(folder.tooltip(copied: false), "Open folder")

        XCTAssertEqual(span("code", "go test").tooltip(copied: false), "Copy")
        XCTAssertEqual(span("commit", "a1b2c3d").tooltip(copied: false), "Copy commit")
        XCTAssertEqual(span("email", "d@x.com").tooltip(copied: false), "Copy email")
        XCTAssertEqual(span("commit", "a1b2c3d").tooltip(copied: true), "Copied")
        XCTAssertEqual(span("commit", "a1b2c3d").action, .copy("a1b2c3d"))
        XCTAssertNil(span("text", "hi").action)
        // A link the core sent without an address does nothing.
        XCTAssertNil(span("link", "x").action)
    }

    // While the reply card types the raw text out (words joined by single spaces), only what
    // is shown so far is drawn; a link half typed is plain text until it is whole.
    func testPrefixFollowsTheTypedWords() {
        let spans = [span("text", "Done", raw: "**Done**", bold: true), span("text", "\nsee "),
                     span("link", "https://x.com", url: "https://x.com"), span("text", " ok")]
        XCTAssertEqual(RichText.prefix(spans, shown: "**Done** see https://x.com ok"), spans)
        let half = RichText.prefix(spans, shown: "**Done** see https://x")
        XCTAssertEqual(half.map(\.text), ["Done", "\nsee ", "https://x"])
        XCTAssertEqual(half.last?.kind, "text")
        XCTAssertEqual(RichText.prefix(spans, shown: "**Do").map(\.text), ["Do"])
        XCTAssertEqual(RichText.prefix(spans, shown: ""), [])
    }

    func testAttributedMarksEachActionableSpan() {
        let spans = [span("text", "see "), span("link", "docs", url: "https://x.com"), span("text", " and "),
                     span("commit", "a1b2c3d"), span("text", "Bold", bold: true)]
        let s = RichText.attributed(spans, font: .systemFont(ofSize: 12), color: .labelColor)
        XCTAssertEqual(s.string, "see docs and a1b2c3dBold")
        let ns = s.string as NSString
        let linkAt = ns.range(of: "docs").location
        XCTAssertEqual(s.attribute(RichText.spanKey, at: linkAt, effectiveRange: nil) as? Int, 1)
        XCTAssertNil(s.attribute(.toolTip, at: linkAt, effectiveRange: nil), "the view shows the tooltip itself")
        XCTAssertNil(s.attribute(RichText.spanKey, at: 0, effectiveRange: nil))
        let commitAt = ns.range(of: "a1b2c3d").location
        XCTAssertEqual(s.attribute(RichText.spanKey, at: commitAt, effectiveRange: nil) as? Int, 3)
        let mono = s.attribute(.font, at: commitAt, effectiveRange: nil) as? NSFont
        XCTAssertTrue(mono?.fontDescriptor.symbolicTraits.contains(.monoSpace) ?? false)
        let bold = s.attribute(.font, at: ns.range(of: "Bold").location, effectiveRange: nil) as? NSFont
        XCTAssertTrue(bold?.fontDescriptor.symbolicTraits.contains(.bold) ?? false)
    }

    // The spans are fetched once per text and sessions, off the main thread; until then the
    // view draws the plain text.
    @MainActor
    func testTheStoreFetchesOncePerText() async {
        let calls = Counter()
        let store = RichTextStore { text, sessions in
            calls.add("\(text)|\(sessions.joined(separator: ","))")
            return [RichSpan(kind: "text", text: text, raw: text, bold: false, url: nil, path: nil, dir: false,
                             line: 0, col: 0, open: [])]
        }
        var changes = 0
        store.onChange = { changes += 1 }
        XCTAssertNil(store.spans("hi", sessions: ["a"]))
        XCTAssertNil(store.spans("hi", sessions: ["a"]), "asked once while it is on its way")
        await waitUntil { store.spans("hi", sessions: ["a"]) != nil }
        XCTAssertEqual(changes, 1, "the panel lays out again when spans arrive")
        XCTAssertEqual(store.spans("hi", sessions: ["a"])?.first?.text, "hi")
        XCTAssertNil(store.spans("hi", sessions: ["b"]), "other sessions resolve paths elsewhere")
        await waitUntil { store.spans("hi", sessions: ["b"]) != nil }
        XCTAssertEqual(calls.all, ["hi|a", "hi|b"])
    }

    // A failed call is not asked again for the same text, and the text shows plain (review of #277:
    // it used to show nothing).
    @MainActor
    func testAFailedFetchKeepsThePlainText() async {
        let calls = Counter()
        let store = RichTextStore { text, _ in
            calls.add(text)
            throw DeskPixieEngineError.callFailed("no")
        }
        XCTAssertNil(store.spans("hi", sessions: []))
        await waitUntil { calls.all.count == 1 }
        try? await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(store.spans("hi", sessions: []), [RichSpan.plain("hi")])
        XCTAssertEqual(calls.all, ["hi"])
    }

    // A row's button skips the click a span took (once, and only right after it).
    @MainActor
    func testARowSkipsTheClickASpanTook() {
        XCTAssertFalse(RichTextClicks.takeSpanPress(now: 10))
        RichTextClicks.spanPressed(at: 10)
        XCTAssertTrue(RichTextClicks.takeSpanPress(now: 10.2))
        XCTAssertFalse(RichTextClicks.takeSpanPress(now: 10.3), "only once")
        RichTextClicks.spanPressed(at: 10)
        XCTAssertFalse(RichTextClicks.takeSpanPress(now: 12), "a stale press doesn't swallow a later click")
    }

    // A settings change (where files open) fetches the spans again, their open plans changed;
    // the old ones show meanwhile, so nothing flashes plain.
    @MainActor
    func testResetFetchesAgain() async {
        let calls = Counter()
        let store = RichTextStore { text, _ in
            calls.add(text)
            return [RichSpan.plain(text)]
        }
        _ = store.spans("hi", sessions: [])
        await waitUntil { store.spans("hi", sessions: []) != nil }
        let before = store.version
        store.reset()
        XCTAssertGreaterThan(store.version, before, "the views draw again")
        XCTAssertEqual(store.spans("hi", sessions: []), [RichSpan.plain("hi")], "the old spans meanwhile")
        await waitUntil { calls.all.count == 2 }
        _ = store.spans("hi", sessions: [])
        try? await Task.sleep(nanoseconds: 30_000_000)
        XCTAssertEqual(calls.all, ["hi", "hi"], "fetched again once")
    }

    // Review of #277: what would launch or run (an app, a script) only shows in Finder.
    func testARevealSpanOnlyShowsInFinder() {
        let app = RichSpan(kind: "path", text: "Tool.app", raw: "Tool.app", bold: false, url: nil, path: "/w/Tool.app",
                           dir: true, reveal: true, line: 0, col: 0, open: [])
        XCTAssertEqual(app.action, .reveal("/w/Tool.app"))
        XCTAssertEqual(app.tooltip(copied: false), "Show in Finder")
        let json = #"{"kind":"path","text":"go.command","raw":"go.command","path":"/w/go.command","reveal":true}"#
        let decoded = try? JSONDecoder().decode(RichSpan.self, from: Data(json.utf8))
        XCTAssertEqual(decoded?.action, .reveal("/w/go.command"))
    }

    @MainActor
    private func waitUntil(_ ok: () -> Bool) async {
        for _ in 0..<200 where !ok() {
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
        XCTAssertTrue(ok())
    }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var calls: [String] = []
    func add(_ s: String) { lock.withLock { calls.append(s) } }
    var all: [String] { lock.withLock { calls } }
}

final class RichTextActionsTests: XCTestCase {
    private final class FakeOS: RichTextPrimitives, @unchecked Sendable {
        var handlers: Set<String> = []
        var opened: [URL] = []
        var revealed: [String] = []
        var copied: [String] = []
        var openFails = false
        func canOpen(_ url: URL) -> Bool { url.isFileURL || handlers.contains(url.scheme ?? "") }
        func open(_ url: URL) -> Bool {
            if openFails { return false }
            opened.append(url)
            return true
        }
        func reveal(_ path: String) { revealed.append(path) }
        func copy(_ text: String) { copied.append(text) }
    }

    private let file = RichSpan(kind: "path", text: "a.go:3", raw: "a.go:3", bold: false, url: nil, path: "/w/a.go",
                                dir: false, line: 3, col: 0, open: ["vscode://file/w/a.go:3", "file:///w/a.go"])

    func testAFileOpensInTheFirstEditorThereIsAnAppFor() {
        let os = FakeOS()
        os.handlers = ["vscode"]
        XCTAssertEqual(RichTextActions(os: os).perform(file, reveal: false), .opened)
        XCTAssertEqual(os.opened, [URL(string: "vscode://file/w/a.go:3")!])
    }

    func testWithoutTheEditorItOpensInTheDefaultApp() {
        let os = FakeOS()
        XCTAssertEqual(RichTextActions(os: os).perform(file, reveal: false), .opened)
        XCTAssertEqual(os.opened, [URL(string: "file:///w/a.go")!])
    }

    func testARevealSpanIsShownNotOpened() {
        let os = FakeOS()
        let app = RichSpan(kind: "path", text: "Tool.app", raw: "Tool.app", bold: false, url: nil, path: "/w/Tool.app",
                           dir: true, reveal: true, line: 0, col: 0, open: ["file:///w/Tool.app"])
        XCTAssertEqual(RichTextActions(os: os).perform(app, reveal: false), .revealed)
        XCTAssertEqual(os.revealed, ["/w/Tool.app"])
        XCTAssertEqual(os.opened, [])
    }

    func testOptionClickShowsItInFinder() {
        let os = FakeOS()
        os.handlers = ["vscode"]
        XCTAssertEqual(RichTextActions(os: os).perform(file, reveal: true), .revealed)
        XCTAssertEqual(os.revealed, ["/w/a.go"])
        XCTAssertEqual(os.opened, [])
    }

    func testNothingOpensIsAFailure() {
        let os = FakeOS()
        os.openFails = true
        XCTAssertEqual(RichTextActions(os: os).perform(file, reveal: false), .failed)
    }

    func testCodeCommitsAndEmailsCopy() {
        let os = FakeOS()
        let commit = RichSpan(kind: "commit", text: "a1b2c3d", raw: "a1b2c3d", bold: false, url: nil, path: nil,
                              dir: false, line: 0, col: 0, open: [])
        XCTAssertEqual(RichTextActions(os: os).perform(commit, reveal: true), .copied)
        XCTAssertEqual(os.copied, ["a1b2c3d"])
    }
}
