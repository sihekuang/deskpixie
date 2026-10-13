import AppKit
import DeskPixieCore

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    // Migrate the old AI Mascot data folder before anything reads it.
    // Property initializers run in order, so this precedes CompositionRoot().
    private let migration = DataMigration.runIfNeeded()
    let composition = CompositionRoot()

    func applicationDidFinishLaunching(_ notification: Notification) {
        composition.coordinator.start()
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
