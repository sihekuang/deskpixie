import AppKit
import DeskPixieCore

/// The tray menu as an AppKit menu, for a secondary click on Pixie (#207): the same rows,
/// built from the same `TrayItem` tree, and every pick goes through the same `perform`.
@MainActor
final class TrayContextMenu: NSObject {
    private let perform: (String) -> Void

    private init(perform: @escaping (String) -> Void) {
        self.perform = perform
    }

    /// Pops the menu up at `event` over `view`; returns once it closes.
    static func popUp(_ items: [TrayItem], with event: NSEvent, for view: NSView, perform: @escaping (String) -> Void) {
        let target = TrayContextMenu(perform: perform)
        let menu = target.menu(items)
        // Synchronous: target (the items' weak target) lives until the menu closes.
        NSMenu.popUpContextMenu(menu, with: event, for: view)
        withExtendedLifetime(target) {}
    }

    private func menu(_ items: [TrayItem]) -> NSMenu {
        let menu = NSMenu()
        menu.autoenablesItems = false
        for item in items {
            switch item {
            case .label(let text):
                let row = NSMenuItem(title: text, action: nil, keyEquivalent: "")
                row.isEnabled = false
                menu.addItem(row)
            case .separator:
                menu.addItem(.separator())
            case .action(let r):
                let row = NSMenuItem(title: r.title, action: #selector(pick(_:)), keyEquivalent: r.shortcut.map(String.init) ?? "")
                row.target = self
                row.representedObject = r.id
                row.isEnabled = r.enabled
                if r.shift { row.keyEquivalentModifierMask = [.command, .shift] }
                if let checked = r.checked { row.state = checked ? .on : .off }
                menu.addItem(row)
            case .submenu(let title, let enabled, let children):
                let row = NSMenuItem(title: title, action: nil, keyEquivalent: "")
                row.isEnabled = enabled
                row.submenu = self.menu(children)
                menu.addItem(row)
            }
        }
        return menu
    }

    @objc private func pick(_ sender: NSMenuItem) {
        if let id = sender.representedObject as? String { perform(id) }
    }
}

/// Opens the Settings scene from outside SwiftUI (Pixie's menu): SwiftUI only hands out
/// `openSettings` inside a view, so the menu bar icon (`MenuBarLabel`) leaves it here.
@MainActor
enum SettingsOpener {
    static var open: (() -> Void)?
}
