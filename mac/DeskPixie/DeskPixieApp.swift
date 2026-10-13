import SwiftUI

@main
struct DeskPixieApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        MenuBarExtra {
            MenuBarMenu(
                appState: appDelegate.composition.appState,
                talk: { appDelegate.composition.coordinator.talk() },
                setState: { appDelegate.composition.coordinator.debugSetState($0) },
                quit: { NSApp.terminate(nil) }
            )
        } label: {
            Image(systemName: "sparkles")
        }
        .menuBarExtraStyle(.menu)
    }
}
