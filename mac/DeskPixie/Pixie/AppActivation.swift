import AppKit
import os

/// Brings a running app forward by bundle id, like a Dock click: the roster's session rows and
/// the bubble's session lines (#114).
enum AppActivation {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "activate")

    static func activate(bundleID id: String) {
        guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: id).first,
              let url = app.bundleURL else { return }
        // Launch Services activates like a Dock click; NSRunningApplication.activate() can be
        // ignored from an inactive accessory app.
        let cfg = NSWorkspace.OpenConfiguration()
        cfg.activates = true
        NSWorkspace.shared.openApplication(at: url, configuration: cfg) { _, error in
            if let error { log.error("activate \(id, privacy: .public): \(String(describing: error), privacy: .public)") }
        }
    }
}
