import SwiftUI

@main
struct DeskPixieApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    var body: some Scene {
        MenuBarExtra {
            MenuBarMenu(appState: appDelegate.composition.appState) { id in
                appDelegate.composition.coordinator.perform(id)
            }
        } label: {
            Image(systemName: "sparkles")
        }
        .menuBarExtraStyle(.menu)

        Settings {
            SettingsPlaceholderView()
        }
    }
}
