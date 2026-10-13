import AppKit

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    let composition = CompositionRoot()

    func applicationDidFinishLaunching(_ notification: Notification) {
        composition.coordinator.start()
    }

    func applicationWillTerminate(_ notification: Notification) {
        composition.coordinator.shutdown()
    }
}
