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
                // From a menu-bar app the window would open behind other apps' windows.
                WindowFocus.openSettings(app: NSApp, open: { openSettings() },
                                         findWindow: { NSApp.windows.first { $0.identifier?.rawValue == "settings" } },
                                         defer: { work in DispatchQueue.main.async { MainActor.assumeIsolated(work) } })
            } else {
                perform(id)
            }
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
            item.keyboardShortcut(KeyEquivalent(key), modifiers: .command)
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
