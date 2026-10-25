import DeskPixieCore
import SwiftUI

/// Tray menu (`.menu` style), rebuilt from `AppState.menuItems` (ai-mascot's tray tree).
/// Settings… opens the Settings scene; every other id goes to `perform`.
struct MenuBarMenu: View {
    let appState: AppState
    let perform: (String) -> Void

    @Environment(\.openSettings) private var openSettings

    var body: some View {
        TrayItemsView(items: appState.menuItems) { id in
            if TrayMenu.route(id) == .settings {
                Self.showSettings(openSettings)
            } else {
                perform(id)
            }
        }
    }

    /// From a menu-bar app the window would open behind other apps' windows.
    static func showSettings(_ open: OpenSettingsAction) {
        WindowFocus.openSettings(app: NSApp, open: { open() },
                                 findWindow: { NSApp.windows.first { $0.identifier?.rawValue == "settings" } },
                                 defer: { work in DispatchQueue.main.async { MainActor.assumeIsolated(work) } })
    }
}

/// The menu bar icon. It is drawn at launch (the menu's own view only when the menu opens), so
/// it is where Pixie's menu (#207) gets SwiftUI's openSettings from.
struct MenuBarLabel: View {
    let image: NSImage
    let tooltip: String

    @Environment(\.openSettings) private var openSettings

    var body: some View {
        let open = openSettings
        Image(nsImage: image)
            .help(tooltip)
            .accessibilityLabel(tooltip)
            .onAppear {
                SettingsOpener.open = { MenuBarMenu.showSettings(open) }
            }
    }
}

/// One menu level; submenus recurse (type-erased, since an opaque type cannot contain itself).
private struct TrayItemsView: View {
    let items: [TrayItem]
    let perform: (String) -> Void

    var body: some View {
        ForEach(Array(items.enumerated()), id: \.offset) { _, item in
            switch item {
            case .label(let text):
                Text(text)
            case .separator:
                Divider()
            case .action(let row):
                RowView(row: row, perform: perform)
            case .submenu(let title, let enabled, let children):
                Menu(title) { AnyView(TrayItemsView(items: children, perform: perform)) }
                    .disabled(!enabled)
            }
        }
    }
}

private struct RowView: View {
    let row: TrayMenu.Row
    let perform: (String) -> Void

    var body: some View {
        if let key = row.shortcut {
            item.keyboardShortcut(KeyEquivalent(key), modifiers: row.shift ? [.command, .shift] : .command)
        } else {
            item
        }
    }

    private var item: some View {
        Group {
            if let checked = row.checked {
                // A menu Toggle renders as a checkmark item; the core owns the value.
                Toggle(row.title, isOn: Binding(get: { checked }, set: { _ in perform(row.id) }))
            } else {
                Button(row.title) { perform(row.id) }
            }
        }
        .disabled(!row.enabled)
    }
}
