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
    }

    func applicationWillTerminate(_ notification: Notification) {
        composition.coordinator.shutdown()
    }
}
