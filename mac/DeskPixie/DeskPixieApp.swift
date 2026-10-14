import DeskPixieCore
import SwiftUI

@main
struct DeskPixieApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    private static let menuBarIcon = MenuBarIcon.imageOrFallback(
        packDirectory: (Bundle.main.resourceURL ?? Bundle.main.bundleURL).appendingPathComponent("mascots/bit"))

    var body: some Scene {
        MenuBarExtra {
            MenuBarMenu(appState: appDelegate.composition.appState) { id in
                appDelegate.composition.coordinator.perform(id)
            }
        } label: {
            // Bit's face as a template image (tinted for light/dark menu bars); sparkles if
            // the bundled pack cannot be read.
            Image(nsImage: Self.menuBarIcon)
                .help(MenuBarIcon.tooltip)
                .accessibilityLabel(MenuBarIcon.tooltip)
        }
        .menuBarExtraStyle(.menu)

        // Tray Settings… (⌘,) opens this scene through `openSettings()` (MenuBarMenu).
        Settings {
            SettingsWindowView(model: SettingsHub.model(for: appDelegate.composition))
        }
    }
}
