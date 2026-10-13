import SwiftUI

/// Tray menu (`.menu` style): status line, talk, debug state switcher, quit.
struct MenuBarMenu: View {
    static let states = ["idle", "listening", "thinking", "delegating", "happy", "error"]

    let appState: AppState
    let talk: () -> Void
    let setState: (String) -> Void
    let quit: () -> Void

    var body: some View {
        Text(appState.status)
        Divider()
        Button("Talk to Bit (⌃⌥Space)", action: talk)
        Menu("State") {
            ForEach(Self.states, id: \.self) { s in
                Button(s == appState.mascotState ? "✓ \(s)" : s) { setState(s) }
            }
        }
        Divider()
        Button("Quit DeskPixie", action: quit)
            .keyboardShortcut("q")
    }
}
