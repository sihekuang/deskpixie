import AppKit
import DeskPixieCore

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    // Nothing here touches the disk or the core: data migration and dp_configure run off the
    // main thread in `EngineCoordinator.start()`, so the menu bar item appears at once.
    let composition = CompositionRoot()

    func applicationDidFinishLaunching(_ notification: Notification) {
        composition.coordinator.start() // returns at once; the core starts in the background
        // MenuBarExtra has no tooltip API: set it on the status item's button once it exists.
        DispatchQueue.main.async { Self.setStatusItemToolTip(MenuBarIcon.tooltip) }
    }

    private static func setStatusItemToolTip(_ text: String) {
        func buttons(in view: NSView?) -> [NSStatusBarButton] {
            guard let view else { return [] }
            return (view as? NSStatusBarButton).map { [$0] } ?? view.subviews.flatMap { buttons(in: $0) }
        }
        for window in NSApp.windows where String(describing: type(of: window)).contains("StatusBar") {
            buttons(in: window.contentView).forEach { $0.toolTip = text }
        }
    }

    func applicationWillTerminate(_ notification: Notification) {
        composition.coordinator.shutdown()
    }
}
