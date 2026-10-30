import DeskPixieCore
import SwiftUI

@main
struct DeskPixieApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) var appDelegate

    private static let menuBarIcon = MenuBarIcon.imageOrFallback(
        packDirectory: (Bundle.main.resourceURL ?? Bundle.main.bundleURL).appendingPathComponent("pixies/bit"))

    var body: some Scene {
        MenuBarExtra {
            MenuBarMenu(appState: appDelegate.composition.appState) { id in
                appDelegate.composition.coordinator.perform(id)
            }
        } label: {
            // The chosen pixie's face as a template image (tinted for light/dark menu bars);
            // Bit's until the pixie loads, sparkles if the bundled pack cannot be read.
            MenuBarLabel(appState: appDelegate.composition.appState, fallback: Self.menuBarIcon,
                         tooltip: MenuBarIcon.tooltip)
        }
        .menuBarExtraStyle(.menu)

        // Tray Settings… (⌘,) opens this scene through `openSettings()` (MenuBarMenu).
        Settings {
            SettingsWindowView(model: SettingsHub.model(for: appDelegate.composition))
        }
    }
}
