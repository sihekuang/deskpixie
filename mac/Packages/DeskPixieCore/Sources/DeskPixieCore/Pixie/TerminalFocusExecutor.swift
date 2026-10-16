import AppKit
import ApplicationServices
import Foundation

/// A terminal to open or show, and its own fallback chain (#116): the core decides every string
/// and the order; this executor only performs the OS actions. Mirrors Go's `agent.FocusPlan`
/// (`core/agent/traits.go`), snake_case keys on the wire.
public struct FocusPlan: Decodable, Equatable, Sendable {
    public let focusURLs: [String]?
    public let raiseScript: [String]?
    public let itermSelect: [String]?
    public let activateApp: String?
    public let killPIDs: [Int]?
    public let newTerminal: NewTerminalAction?

    private enum CodingKeys: String, CodingKey {
        case focusURLs = "focus_urls"
        case raiseScript = "raise_script"
        case itermSelect = "iterm_select"
        case activateApp = "activate_app"
        case killPIDs = "kill_pids"
        case newTerminal = "new_terminal"
    }

    public init(focusURLs: [String]? = nil, raiseScript: [String]? = nil, itermSelect: [String]? = nil,
                activateApp: String? = nil, killPIDs: [Int]? = nil, newTerminal: NewTerminalAction? = nil) {
        self.focusURLs = focusURLs
        self.raiseScript = raiseScript
        self.itermSelect = itermSelect
        self.activateApp = activateApp
        self.killPIDs = killPIDs
        self.newTerminal = newTerminal
    }
}

/// The new terminal a FocusPlan falls back to, if every earlier rung fails.
public struct NewTerminalAction: Decodable, Equatable, Sendable {
    public let kind: String // "url" | "applescript"
    public let url: String?
    public let script: [String]?

    public init(kind: String, url: String? = nil, script: [String]? = nil) {
        self.kind = kind
        self.url = url
        self.script = script
    }
}

/// The OS actions a FocusPlan's rungs perform; Go decides what to try, this is only how.
public protocol TerminalFocusPrimitives: Sendable {
    func openURL(_ url: String) -> Bool
    /// Runs an AppleScript (one source line per array entry); nil on failure, else its trimmed
    /// stdout (which may be empty).
    func runAppleScript(_ lines: [String]) -> String?
    func isAccessibilityTrusted(prompt: Bool) -> Bool
    func killPID(_ pid: Int32)
}

/// Walks a FocusPlan's rungs until one shows the terminal, then reports the outcome back to the
/// core (`agent.reportFocusOutcome`) so a failure reaches the tray through the same path as any
/// other terminal failure. `itermSelect` and `activateApp` have no fallback — each is the whole
/// plan when it is set. Otherwise: a focus URL, then (if Accessibility allows) raising the
/// titled window, then ending the attached clients and opening a new terminal.
///
/// Not actor-isolated: `execute(_:)` can block for seconds (NSAppleScript launching an app, an
/// Automation consent dialog, a hung target's Apple Event timeout), so the caller runs it off the
/// main thread — same rule as every other core call that can touch the disk or spawn a process.
/// `promptedAccessibility` is the only mutable state, under a lock for that reason.
public final class TerminalFocusExecutor: @unchecked Sendable {
    private static let failureDetail = "could not show the terminal"

    private let primitives: TerminalFocusPrimitives
    private let reportOutcome: @Sendable (Bool, String) -> Void
    private let lock = NSLock()
    /// Accessibility is prompted at most once per executor (per app launch): a repeat ask would
    /// nag for a permission the person already answered.
    private var promptedAccessibility = false

    public init(primitives: TerminalFocusPrimitives, reportOutcome: @escaping @Sendable (Bool, String) -> Void) {
        self.primitives = primitives
        self.reportOutcome = reportOutcome
    }

    public func execute(_ plan: FocusPlan) {
        if let script = plan.itermSelect, !script.isEmpty {
            report(primitives.runAppleScript(script) != nil, app: "iTerm")
            return
        }
        if let app = plan.activateApp, !app.isEmpty {
            report(primitives.openURL(app), app: app)
            return
        }
        for url in plan.focusURLs ?? [] where primitives.openURL(url) {
            report(true)
            return
        }
        if let raise = plan.raiseScript, !raise.isEmpty, raised(raise) {
            report(true)
            return
        }
        for pid in plan.killPIDs ?? [] {
            primitives.killPID(Int32(pid))
        }
        if let newTerminal = plan.newTerminal {
            report(open(newTerminal), app: newTerminal.kind == "applescript" ? "iTerm" : "Warp")
            return
        }
        report(false)
    }

    /// Raises the window the script names; "ok" is the only success (System Events' AppleScript
    /// returns "none" when it ran fine but found no such window — not a failure to report, just
    /// nothing to raise).
    private func raised(_ script: [String]) -> Bool {
        let firstPrompt = lock.withLock { () -> Bool in
            let first = !promptedAccessibility
            promptedAccessibility = true
            return first
        }
        let trusted = primitives.isAccessibilityTrusted(prompt: firstPrompt)
        return trusted && primitives.runAppleScript(script) == "ok"
    }

    private func open(_ newTerminal: NewTerminalAction) -> Bool {
        switch newTerminal.kind {
        case "url": return primitives.openURL(newTerminal.url ?? "")
        case "applescript": return primitives.runAppleScript(newTerminal.script ?? []) != nil
        default: return false
        }
    }

    /// `app`, when given, names what could not be shown (e.g. "Warp", "iTerm", an app path) so
    /// the tray line matches exactly what it said before this rung moved to Swift: Go's
    /// `TerminalFailureStatus` keeps only the text up to the first colon of a "could not open "
    /// detail.
    private func report(_ success: Bool, app: String? = nil) {
        guard !success else {
            reportOutcome(true, "")
            return
        }
        let detail = app.map { "could not open \($0): \(Self.failureDetail)" } ?? Self.failureDetail
        reportOutcome(false, detail)
    }
}

/// The real primitives: NSWorkspace to open, NSAppleScript to run, Accessibility's own trust
/// check, and a HUP signal to end an attached client (not test-covered beyond compiling — there
/// is no way to drive real AppleScript/Accessibility/process state from a unit test).
public struct SystemTerminalFocusPrimitives: TerminalFocusPrimitives {
    public init() {}

    public func openURL(_ url: String) -> Bool {
        if let u = URL(string: url), u.scheme != nil {
            return NSWorkspace.shared.open(u)
        }
        return NSWorkspace.shared.open(URL(fileURLWithPath: url))
    }

    public func runAppleScript(_ lines: [String]) -> String? {
        guard let script = NSAppleScript(source: lines.joined(separator: "\n")) else { return nil }
        var error: NSDictionary?
        let result = script.executeAndReturnError(&error)
        guard error == nil else { return nil }
        return (result.stringValue ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    public func isAccessibilityTrusted(prompt: Bool) -> Bool {
        // The literal value of kAXTrustedCheckOptionPrompt (a CFString constant): spelled out
        // rather than read from the C global, which Swift 6 flags as not concurrency-safe.
        AXIsProcessTrustedWithOptions(["AXTrustedCheckOptionPrompt": prompt] as CFDictionary)
    }

    public func killPID(_ pid: Int32) {
        kill(pid, SIGHUP)
    }
}
