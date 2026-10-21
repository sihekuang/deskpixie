import AppKit
import ApplicationServices
import AVFoundation
import Carbon
import DeskPixieCore
import UserNotifications

/// Reads and asks for each macOS permission (#210). Reading never shows a dialog; asking shows
/// macOS's own. Not test-covered beyond compiling: TCC can't be driven from a unit test.
enum SystemPermissions {
    /// The Automation targets installed on this Mac (System Events always is).
    static func installedTargets() -> Set<AutomationTarget> {
        Set(AutomationTarget.allCases.filter { NSWorkspace.shared.urlForApplication(withBundleIdentifier: $0.bundleID) != nil })
    }

    /// Where `p` stands. Automation calls into the Apple Event manager: run it off main.
    static func access(_ p: AppPermission) async -> PermissionAccess {
        switch p {
        case .microphone:
            switch AVCaptureDevice.authorizationStatus(for: .audio) {
            case .authorized: return .granted
            case .notDetermined: return .notDetermined
            default: return .denied
            }
        case .notifications:
            switch await UNUserNotificationCenter.current().notificationSettings().authorizationStatus {
            case .authorized, .provisional, .ephemeral: return .granted
            case .denied: return .denied
            default: return .notDetermined
            }
        case .accessibility:
            return AXIsProcessTrusted() ? .granted : .notDetermined
        case .automation(let app):
            return await Task.detached { automation(app, ask: false) }.value
        }
    }

    /// Asks macOS for `p` and returns once it was answered (Accessibility: once its dialog is up,
    /// since the answer is given in System Settings). The Automation answer, which can't be read
    /// back once the app quits; nil for the others, which can.
    static func ask(_ p: AppPermission) async -> PermissionAccess? {
        switch p {
        case .microphone:
            _ = await AVCaptureDevice.requestAccess(for: .audio)
        case .notifications:
            // The same options SystemNotificationSink asks for.
            _ = try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert])
        case .accessibility:
            // The literal value of kAXTrustedCheckOptionPrompt (see SystemTerminalFocusPrimitives).
            _ = AXIsProcessTrustedWithOptions(["AXTrustedCheckOptionPrompt": true] as CFDictionary)
        case .automation(let app):
            return await askAutomation(app)
        }
        return nil
    }

    /// macOS only asks about an app that is running: one that isn't is opened hidden for the
    /// question, then quit again.
    private static func askAutomation(_ app: AutomationTarget) async -> PermissionAccess {
        let wasRunning = !NSRunningApplication.runningApplications(withBundleIdentifier: app.bundleID).isEmpty
        var opened: NSRunningApplication?
        if !wasRunning, let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: app.bundleID) {
            let config = NSWorkspace.OpenConfiguration()
            config.activates = false
            config.hides = true
            config.addsToRecentItems = false
            opened = try? await NSWorkspace.shared.openApplication(at: url, configuration: config)
        }
        // A just-opened app takes a moment to take Apple Events: try for up to 5 s.
        var answer = PermissionAccess.notDetermined
        for _ in 0..<20 {
            if let a = await Task.detached(operation: { automationAnswer(app, ask: true) }).value {
                answer = a
                break
            }
            try? await Task.sleep(for: .milliseconds(250))
        }
        // Still hidden in the background: nobody started using it meanwhile.
        if let opened, opened.isHidden, !opened.isActive { opened.terminate() }
        return answer
    }

    private static func automation(_ app: AutomationTarget, ask: Bool) -> PermissionAccess {
        automationAnswer(app, ask: ask) ?? .notDetermined
    }

    /// nil when the app isn't running, so there is nothing to ask (or read) yet. Blocks while
    /// macOS's dialog is up.
    private static func automationAnswer(_ app: AutomationTarget, ask: Bool) -> PermissionAccess? {
        guard let target = NSAppleEventDescriptor(bundleIdentifier: app.bundleID).aeDesc else { return nil }
        switch AEDeterminePermissionToAutomateTarget(target, typeWildCard, typeWildCard, ask) {
        case noErr: return .granted
        case OSStatus(errAEEventNotPermitted): return .denied
        case OSStatus(procNotFound): return nil
        default: return .notDetermined
        }
    }
}
