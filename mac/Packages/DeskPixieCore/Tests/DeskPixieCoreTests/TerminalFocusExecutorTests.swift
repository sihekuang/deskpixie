import Foundation
import XCTest
@testable import DeskPixieCore

@MainActor
final class TerminalFocusExecutorTests: XCTestCase {
    /// Records every primitive call (in order, tagged) and answers from canned per-call results;
    /// mirrors Go's `psRunner` in `reuse_test.go` — one fake covers all four primitives.
    final class FakeTerminalFocusPrimitives: TerminalFocusPrimitives, @unchecked Sendable {
        private let lock = NSLock()
        private var _log: [String] = []
        private var _openURLResults: [String: Bool] = [:]
        private var _appleScriptResult: String?
        private var _accessibilityTrusted = true
        private var _accessibilityPrompts: [Bool] = []

        var log: [String] { lock.withLock { _log } }
        var accessibilityPrompts: [Bool] { lock.withLock { _accessibilityPrompts } }

        func answerOpenURL(_ url: String, with ok: Bool) { lock.withLock { _openURLResults[url] = ok } }
        func answerAppleScript(_ result: String?) { lock.withLock { _appleScriptResult = result } }
        func answerAccessibilityTrusted(_ trusted: Bool) { lock.withLock { _accessibilityTrusted = trusted } }

        func openURL(_ url: String) -> Bool {
            lock.withLock {
                _log.append("open:\(url)")
                return _openURLResults[url] ?? false
            }
        }

        func runAppleScript(_ lines: [String]) -> String? {
            lock.withLock {
                _log.append("script:\(lines.joined(separator: "|"))")
                return _appleScriptResult
            }
        }

        func isAccessibilityTrusted(prompt: Bool) -> Bool {
            lock.withLock {
                _accessibilityPrompts.append(prompt)
                _log.append("trust:\(prompt)")
                return _accessibilityTrusted
            }
        }

        func killPID(_ pid: Int32) {
            lock.withLock { _log.append("kill:\(pid)") }
        }
    }

    /// Runs plan against a fresh executor (unless `reuse` is given, for the accessibility-nag
    /// test) and returns the fake plus the reported outcome.
    private func run(_ plan: FocusPlan, reuse executor: TerminalFocusExecutor? = nil,
                      fake: FakeTerminalFocusPrimitives = FakeTerminalFocusPrimitives())
        -> (fake: FakeTerminalFocusPrimitives, outcome: (success: Bool, detail: String)?) {
        var outcome: (success: Bool, detail: String)?
        let exec = executor ?? TerminalFocusExecutor(primitives: fake) { outcome = ($0, $1) }
        exec.execute(plan)
        return (fake, outcome)
    }

    func testItermSelectAloneHasNoFallback() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerAppleScript(nil)
        let (_, outcome) = run(FocusPlan(itermSelect: ["tell application \"iTerm\""]), fake: fake)
        XCTAssertEqual(fake.log, ["script:tell application \"iTerm\""])
        XCTAssertEqual(outcome?.success, false)
    }

    func testActivateAppAloneHasNoFallback() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerOpenURL("/Applications/Warp.app", with: false)
        let (_, outcome) = run(FocusPlan(activateApp: "/Applications/Warp.app"), fake: fake)
        XCTAssertEqual(fake.log, ["open:/Applications/Warp.app"])
        XCTAssertEqual(outcome?.success, false)
    }

    func testFocusURLsTriedInOrderUntilOneSucceeds() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerOpenURL("a", with: false)
        fake.answerOpenURL("b", with: true)
        let (_, outcome) = run(FocusPlan(focusURLs: ["a", "b", "c"]), fake: fake)
        XCTAssertEqual(fake.log, ["open:a", "open:b"])
        XCTAssertEqual(outcome?.success, true)
    }

    func testAllFocusURLsFailFallsThroughToRaise() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerOpenURL("a", with: false)
        fake.answerAppleScript("ok")
        let (_, outcome) = run(FocusPlan(focusURLs: ["a"], raiseScript: ["raise"]), fake: fake)
        XCTAssertEqual(fake.log, ["open:a", "trust:true", "script:raise"])
        XCTAssertEqual(outcome?.success, true)
        XCTAssertFalse(fake.log.contains { $0.hasPrefix("kill:") })
    }

    func testRaiseScriptSucceedsOnlyWhenStdoutIsExactlyOk() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerAppleScript("none") // ran fine, found nothing: not success, falls through
        fake.answerOpenURL("fake://new", with: true)
        let plan = FocusPlan(raiseScript: ["raise"], killPIDs: [1], newTerminal: NewTerminalAction(kind: "url", url: "fake://new"))
        let (_, outcome) = run(plan, fake: fake)
        XCTAssertEqual(fake.log, ["trust:true", "script:raise", "kill:1", "open:fake://new"])
        XCTAssertEqual(outcome?.success, true)
    }

    func testAccessibilityPromptedOnceThenNotNagged() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerAppleScript("ok")
        var outcome: (success: Bool, detail: String)?
        let exec = TerminalFocusExecutor(primitives: fake) { outcome = ($0, $1) }
        exec.execute(FocusPlan(raiseScript: ["raise"]))
        exec.execute(FocusPlan(raiseScript: ["raise"]))
        XCTAssertEqual(fake.accessibilityPrompts, [true, false])
        XCTAssertEqual(outcome?.success, true)
    }

    func testWarpFallbackKillsThenOpensNewTerminal() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerOpenURL("fake://new", with: true)
        let plan = FocusPlan(killPIDs: [1, 2], newTerminal: NewTerminalAction(kind: "url", url: "fake://new"))
        let (_, outcome) = run(plan, fake: fake)
        XCTAssertEqual(fake.log, ["kill:1", "kill:2", "open:fake://new"])
        XCTAssertEqual(outcome?.success, true)
    }

    func testNewTerminalApplescriptKind() {
        let fake = FakeTerminalFocusPrimitives()
        fake.answerAppleScript("")
        let plan = FocusPlan(killPIDs: [1], newTerminal: NewTerminalAction(kind: "applescript", script: ["tell application \"iTerm\""]))
        let (_, outcome) = run(plan, fake: fake)
        XCTAssertEqual(fake.log, ["kill:1", "script:tell application \"iTerm\""])
        XCTAssertEqual(outcome?.success, true)
    }

    func testNothingApplicableReportsFailure() {
        let fake = FakeTerminalFocusPrimitives()
        let (_, outcome) = run(FocusPlan(), fake: fake)
        XCTAssertEqual(fake.log, [])
        XCTAssertEqual(outcome?.success, false)
    }
}
