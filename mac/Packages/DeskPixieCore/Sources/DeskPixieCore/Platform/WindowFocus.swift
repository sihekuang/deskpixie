import AppKit

/// The app-activation calls `WindowFocus` makes (NSApplication; a fake in tests).
@MainActor
public protocol AppActivation: AnyObject {
    @discardableResult
    func setActivationPolicy(_ activationPolicy: NSApplication.ActivationPolicy) -> Bool
    func activate()
}

extension NSApplication: AppActivation {}

/// The window calls `WindowFocus` makes (NSWindow; a fake in tests).
@MainActor
public protocol FocusableWindow: AnyObject {
    func makeKeyAndOrderFront(_ sender: Any?)
    func orderFrontRegardless()
}

extension NSWindow: FocusableWindow {}

/// Brings a window the user asked for (tray Settings…, tray Canvas) in front of other apps'
/// windows. DeskPixie is a menu-bar (LSUIElement) app, so it is not active when a tray item
/// runs: it activates first, then makes the window key and orders it front regardless (the
/// cooperative activation of macOS 14 may still be settling).
@MainActor
public enum WindowFocus {
    /// `showInDock`: switch to `.regular` first (the Settings window shows a Dock icon and is in
    /// ⌘-Tab while open; it switches back to `.accessory` on close).
    public static func bringForward(_ window: (any FocusableWindow)?, app: any AppActivation, showInDock: Bool) {
        if showInDock { app.setActivationPolicy(.regular) }
        app.activate()
        guard let window else { return }
        window.makeKeyAndOrderFront(nil)
        window.orderFrontRegardless()
    }

    /// Tray Settings…: activate, ask SwiftUI to open the Settings scene (`open`), and on the next
    /// main-loop turn (`defer`), once the window exists, bring it forward.
    public static func openSettings(app: any AppActivation, open: () -> Void,
                                    findWindow: @escaping @MainActor () -> (any FocusableWindow)?,
                                    defer later: (@escaping @MainActor () -> Void) -> Void) {
        app.setActivationPolicy(.regular)
        app.activate()
        open()
        later { bringForward(findWindow(), app: app, showInDock: true) }
    }
}
