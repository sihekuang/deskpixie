import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// V-043 / D-449 / D-494: the whole tray menu as the user reads it, built from a representative
/// `app.state`; where each id goes; and how gestures on Bit are routed (click → `pixie.click`,
/// double-click → talk).
@MainActor
final class TrayLabelsAndRoutingTests: XCTestCase {
    /// Indented labels: "> " for a submenu, "[x]"/"[ ]" for checkmarks, "(off)" for disabled.
    private func render(_ items: [TrayItem], depth: Int = 0) -> [String] {
        let pad = String(repeating: "  ", count: depth)
        return items.flatMap { item -> [String] in
            switch item {
            case .label(let t): return ["\(pad)\(t) (off)"]
            case .separator: return ["\(pad)---"]
            case .action(let r):
                let check = r.checked.map { $0 ? "[x] " : "[ ] " } ?? ""
                let key = r.shortcut.map { " ⌘\($0)" } ?? ""
                return ["\(pad)\(check)\(r.title)\(key)\(r.enabled ? "" : " (off)")"]
            case .submenu(let t, let enabled, let children):
                return ["\(pad)> \(t)\(enabled ? "" : " (off)")"] + render(children, depth: depth + 1)
            }
        }
    }

    func testFullMenuLabels() throws {
        let s = try JSONDecoder().decode(TrayAppState.self, from: Data(#"""
        {"state":"idle","packs":[{"id":"bit","name":"Bit"}],"pack":"bit","scale":8,"fade":true,"talking":false,
         "canvasRecent":[{"slug":"q3","title":"Q3 revenue"}],
         "agent":{"enabled":true,"jobs":[{"id":"job_1","label":"Fix the login bug — working, 2 min"},
                                         {"id":"job_2","label":"Write tests — waiting, 10 s"}]},
         "statusLines":["Agent: ready"],"hotkey":"ctrl+alt+Space"}
        """#.utf8))
        let items = TrayMenu.build(s, localStatusLines: ["Hotkey unavailable: ⌃⌥Space"], micModeAvailable: true)
        XCTAssertEqual(render(items), [
            "Hotkey unavailable: ⌃⌥Space (off)", "---",
            "Agent: ready (off)", "---",
            "> State",
            "  [x] idle", "  [ ] listening", "  [ ] thinking", "  [ ] delegating", "  [ ] happy", "  [ ] error",
            "[ ] Simulate talking",
            "> Size",
            "  [ ] Small (×4)", "  [ ] Medium (×6)", "  [x] Large (×8)", "  [ ] Huge (×12)",
            "Reset position",
            "[x] Proximity fade",
            "> Pixie",
            "  [x] Bit",
            "Reload packs",
            "> Canvas",
            "  Open canvas",
            "  > Recent",
            "    Q3 revenue",
            "  Open pages folder",
            "> Agent",
            "  Fix the login bug — working, 2 min (off)",
            "  Write tests — waiting, 10 s (off)",
            "  > Cancel job",
            "    Fix the login bug — working, 2 min",
            "    Write tests — waiting, 10 s",
            "  Open agent terminal",
            "  Restart agent",
            "---",
            "Mic Mode (Voice Isolation)…",
            "Settings… ⌘,",
            "---",
            "Quit",
        ])
    }

    func testRoutes() {
        XCTAssertEqual(TrayMenu.route("quit"), .quit)
        XCTAssertEqual(TrayMenu.route("settings"), .settings)
        XCTAssertEqual(TrayMenu.route("mic-mode"), .micMode)
        XCTAssertEqual(TrayMenu.route("agent:open"), .core("agent:open"))
        XCTAssertEqual(TrayMenu.route("agent:restart"), .core("agent:restart"))
        XCTAssertEqual(TrayMenu.route("agent:cancel:job_1"), .core("agent:cancel:job_1"))
        XCTAssertEqual(TrayMenu.route("canvas:open"), .core("canvas:open", focusCanvas: true))
        XCTAssertEqual(TrayMenu.route("canvas:page:q3"), .core("canvas:page:q3", focusCanvas: true))
        XCTAssertEqual(TrayMenu.route("state:happy"), .core("state:happy"))
    }

    // MARK: Gestures

    private final class RecordingEngine: CoreEngine, @unchecked Sendable {
        let lock = NSLock()
        private var _calls: [String] = []
        private var _hotkeys = 0
        var calls: [String] { lock.withLock { _calls } }
        var hotkeys: Int { lock.withLock { _hotkeys } }

        func call(_ method: String, _ args: any Encodable) throws -> Data {
            lock.withLock { _calls.append(method) }
            return Data("null".utf8)
        }
        func hotkey() { lock.withLock { _hotkeys += 1 } }
        func configure(_ c: EngineConfig) throws {}
        func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool { true }
        func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) { (0, false) }
        func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {}
        func micFailed(_ reason: String) {}
        func pollEvent() -> EngineEvent? { nil }
        func packJSON() -> Data? { nil }
        func shutdown() {}
    }

    private func waitFor(_ cond: () -> Bool) async {
        for _ in 0..<100 where !cond() { try? await Task.sleep(nanoseconds: 10_000_000) }
    }

    private let p = CGPoint(x: 10, y: 10)

    func testSingleClickCallsPixieClick() async {
        let e = RecordingEngine()
        var talks = 0
        let r = PixieGestureRouter(engine: e)
        r.onDoubleClick = { talks += 1 }
        XCTAssertEqual(r.down(at: p, time: 1.0, leftButton: true), .none)
        XCTAssertTrue(r.pointerActive)
        XCTAssertEqual(r.up(at: p, time: 1.05, leftButton: true), .scheduleClickExpiry)
        XCTAssertFalse(r.pointerActive)
        XCTAssertEqual(r.expire(now: 1.2), .none, "still inside the double-click window")
        XCTAssertEqual(r.expire(now: 1.4), .openedTerminal)
        await waitFor { e.calls == ["pixie.click"] }
        XCTAssertEqual(e.calls, ["pixie.click"])
        XCTAssertEqual(talks, 0)
        XCTAssertEqual(e.hotkeys, 0)
    }

    func testDoubleClickTalks() async throws {
        let e = RecordingEngine()
        var talks = 0
        let r = PixieGestureRouter(engine: e)
        r.onDoubleClick = { talks += 1 }
        _ = r.down(at: p, time: 1.0, leftButton: true)
        _ = r.up(at: p, time: 1.05, leftButton: true)
        _ = r.down(at: p, time: 1.15, leftButton: true)
        XCTAssertEqual(r.up(at: p, time: 1.2, leftButton: true), .talked)
        XCTAssertEqual(talks, 1)
        XCTAssertEqual(r.expire(now: 2), .none, "a double is never also a click")
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertTrue(e.calls.isEmpty)
    }

    func testDoubleClickWithoutAHandlerSendsTheHotkey() {
        let e = RecordingEngine()
        let r = PixieGestureRouter(engine: e)
        _ = r.down(at: p, time: 1.0, leftButton: true)
        _ = r.up(at: p, time: 1.05, leftButton: true)
        _ = r.down(at: p, time: 1.15, leftButton: true)
        XCTAssertEqual(r.up(at: p, time: 1.2, leftButton: true), .talked)
        XCTAssertEqual(e.hotkeys, 1)
    }

    func testDragStartsAWindowDragAndIsNoClick() async throws {
        let e = RecordingEngine()
        let r = PixieGestureRouter(engine: e)
        _ = r.down(at: p, time: 1.0, leftButton: true)
        XCTAssertEqual(r.dragged(to: CGPoint(x: 11, y: 11)), .none, "within the 3 pt slop")
        XCTAssertEqual(r.dragged(to: CGPoint(x: 20, y: 10)), .beginDrag)
        XCTAssertTrue(r.pointerActive)
        XCTAssertEqual(r.up(at: CGPoint(x: 20, y: 10), time: 1.3, leftButton: true), .none)
        XCTAssertEqual(r.expire(now: 2), .none)
        try await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertTrue(e.calls.isEmpty)
    }

    func testRightClickIsIgnored() {
        let r = PixieGestureRouter(engine: RecordingEngine())
        XCTAssertEqual(r.down(at: p, time: 1, leftButton: false), .none)
        XCTAssertFalse(r.pointerActive)
    }
}
