import AppKit
import DeskPixieCore
import SwiftUI

/// The Settings window (tray Settings… / ⌘,): six toolbar tabs rendered from the core's
/// settings snapshot. Remembers the last tab (`DeskPixieSettingsTab`) and its frame
/// (`DeskPixieSettingsWindow`); reloads when it becomes key; shows a dock icon while open.
struct SettingsWindowView: View {
    static let tabKey = "DeskPixieSettingsTab"
    static let frameName = "DeskPixieSettingsWindow"

    let model: SettingsModel

    @AppStorage(SettingsWindowView.tabKey) private var tab = 0
    @State private var observers = WindowObservers()

    var body: some View {
        TabView(selection: Binding(
            get: { min(max(tab, 0), SettingsLayout.tabs.count - 1) },
            set: { tab = $0; model.recordingPath = nil }
        )) {
            ForEach(Array(SettingsLayout.tabs.enumerated()), id: \.offset) { index, t in
                SettingsTabView(model: model, section: t.section)
                    .tabItem { Label(t.title, systemImage: t.symbol) }
                    .tag(index)
            }
        }
        .background(VisualEffectBackground().ignoresSafeArea())
        .background(WindowAccessor { window in observers.attach(window, model: model) })
        .onAppear {
            // .regular lets the window become key and show in ⌘-Tab; back to .accessory
            // (menu-bar only) when it closes (Howl's AppDelegate.showSettingsWindow).
            NSApp.setActivationPolicy(.regular)
            NSApp.activate()
            model.load()
        }
    }
}

/// Watches the hosting window: becoming key reloads (hand edits, a key added to `.env`);
/// closing commits typed text, stops recording and hides the dock icon again.
@MainActor
private final class WindowObservers {
    private var tokens: [NSObjectProtocol] = []
    private weak var window: NSWindow?

    func attach(_ window: NSWindow, model: SettingsModel) {
        guard self.window !== window else { return }
        tokens.forEach(NotificationCenter.default.removeObserver)
        self.window = window
        window.identifier = NSUserInterfaceItemIdentifier("settings")
        window.setFrameAutosaveName(SettingsWindowView.frameName)
        window.level = .normal
        window.collectionBehavior.insert([.moveToActiveSpace, .fullScreenAuxiliary])
        let center = NotificationCenter.default
        tokens = [
            center.addObserver(forName: NSWindow.didBecomeKeyNotification, object: window, queue: .main) { _ in
                MainActor.assumeIsolated { model.focusGained() }
            },
            center.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { [weak window] _ in
                MainActor.assumeIsolated {
                    window?.makeFirstResponder(nil)
                    model.commitPendingEdits()
                    model.recordingPath = nil
                    NSApp.setActivationPolicy(.accessory)
                }
            },
        ]
    }
}

/// One tab: the file-error banner, a grid of label / control / note rows, the extra rows,
/// the warning (or notice) line and the Restart bar.
struct SettingsTabView: View {
    let model: SettingsModel
    let section: SettingsSection

    var body: some View {
        SettingsPane {
            if let banner = model.bannerText {
                SettingsStrip(symbol: "exclamationmark.triangle.fill", text: banner, buttonTitle: "Open File",
                              buttonHelp: "Shows settings.json in Finder.") { model.openFile() }
            }
            if model.snapshot == nil {
                Text(model.loadError.map { "Couldn't read settings: \($0)" } ?? "Loading…")
                    .foregroundStyle(.secondary)
            } else {
                grid
            }
            if let notice = model.noticeText {
                NoteText(text: notice, isError: true)
                    .accessibilityAddTraits(.isStaticText)
            }
            if let restart = model.restartText {
                SettingsStrip(symbol: nil, text: restart, buttonTitle: "Restart",
                              buttonHelp: "Restarts DeskPixie to apply the changes.") { model.restart() }
            }
        }
    }

    private var grid: some View {
        Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 8, verticalSpacing: 4) {
            ForEach(model.fields(in: section), id: \.path) { field in
                let spec = model.spec(field)
                row(label: spec.label) {
                    SettingsControlView(model: model, spec: spec)
                        .disabled(!model.enabled)
                } note: {
                    NoteText(text: spec.note.text, isError: spec.note.isError)
                }
            }
            ForEach(Array(SettingsLayout.extras(section).enumerated()), id: \.offset) { _, extra in
                extraRow(extra)
            }
        }
    }

    @ViewBuilder
    private func extraRow(_ extra: SettingsLayout.Extra) -> some View {
        switch extra {
        case .button(let label, let help, let title, let action):
            row(label: SettingsLayout.labelText(label)) {
                Button(title) {
                    switch action {
                    case .resetPosition: model.resetPosition()
                    case .openFile: model.openFile()
                    }
                }
                .disabled(!model.enabled)
                .help(help)
            } note: {
                NoteText(text: help, isError: false)
            }
        case .secret(let id, let label, let help):
            row(label: SettingsLayout.labelText(label)) {
                Text(model.secretText(id))
                    .accessibilityLabel(label)
                    .accessibilityHint(help)
            } note: {
                NoteText(text: help, isError: false)
            }
        }
    }

    @ViewBuilder
    private func row<Control: View, Note: View>(label: String, @ViewBuilder control: () -> Control,
                                                @ViewBuilder note: () -> Note) -> some View {
        GridRow {
            Text(label)
                .gridColumnAlignment(.trailing)
                .accessibilityHidden(true)
            control()
                .gridColumnAlignment(.leading)
        }
        .padding(.top, 10)
        GridRow {
            Color.clear.gridCellUnsizedAxes([.horizontal, .vertical])
            note()
        }
    }
}
