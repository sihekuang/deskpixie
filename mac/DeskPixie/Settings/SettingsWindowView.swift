import AppKit
import DeskPixieCore
import SwiftUI

/// The Settings window (tray Settings… / ⌘,), laid out like Howl's: a sidebar listing the
/// core's settings sections as coloured icon rows, and a scrolling detail panel showing the
/// selected section. Remembers the last page (`DeskPixieSettingsTab`) and its frame
/// (`DeskPixieSettingsWindow`); reloads when it becomes key; shows a dock icon while open.
struct SettingsWindowView: View {
    static let tabKey = "DeskPixieSettingsTab"
    static let frameName = "DeskPixieSettingsWindow"

    let model: SettingsModel

    @AppStorage(SettingsWindowView.tabKey) private var tab = 0
    @State private var observers = WindowObservers()

    /// The remembered index, clamped to the tabs that exist.
    private var selectedIndex: Int { min(max(tab, 0), SettingsLayout.tabs.count - 1) }

    var body: some View {
        HStack(spacing: 0) {
            List(selection: Binding<Int?>(
                get: { selectedIndex },
                set: { new in
                    guard let new, new != selectedIndex else { return }
                    tab = new
                    model.recordingPath = nil
                }
            )) {
                ForEach(Array(SettingsLayout.tabs.enumerated()), id: \.offset) { index, t in
                    SettingsSidebarRow(tab: t).tag(index)
                }
            }
            .listStyle(.sidebar)
            .frame(width: 200)

            SettingsDetailView(model: model, tab: SettingsLayout.tabs[selectedIndex])
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(
                    VisualEffectBackground(material: .sidebar, blendingMode: .behindWindow)
                        .ignoresSafeArea()
                )
        }
        .frame(minWidth: 720, minHeight: 520)
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

extension SettingsLayout.TabTint {
    /// The SwiftUI colour of the same name.
    var color: Color {
        switch self {
        case .gray: return .gray
        case .red: return .red
        case .purple: return .purple
        case .blue: return .blue
        case .orange: return .orange
        case .teal: return .teal
        case .green: return .green
        case .mint: return .mint
        case .pink: return .pink
        case .indigo: return .indigo
        case .cyan: return .cyan
        }
    }
}

/// A tab's SF Symbol as a white glyph on a rounded tile of its colour (Howl's sidebar icons).
private struct SettingsIconTile: View {
    let tab: SettingsLayout.Tab
    let size: CGFloat
    let glyph: CGFloat
    let radius: CGFloat

    var body: some View {
        Image(systemName: tab.symbol)
            .font(.system(size: glyph, weight: .semibold))
            .foregroundStyle(.white)
            .frame(width: size, height: size)
            .background(
                RoundedRectangle(cornerRadius: radius, style: .continuous)
                    .fill(tab.tint.color.gradient)
            )
            .accessibilityHidden(true)
    }
}

/// One sidebar row (Howl's `SidebarRow`).
private struct SettingsSidebarRow: View {
    let tab: SettingsLayout.Tab

    var body: some View {
        Label {
            Text(tab.title)
        } icon: {
            SettingsIconTile(tab: tab, size: 22, glyph: 12, radius: 6)
        }
    }
}

/// The selected page: its header, then the tab's rows, scrolling when taller than the window.
private struct SettingsDetailView: View {
    let model: SettingsModel
    let tab: SettingsLayout.Tab

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                HStack(spacing: 10) {
                    SettingsIconTile(tab: tab, size: 28, glyph: 14, radius: 7)
                    Text(tab.title)
                        .font(.title2)
                        .fontWeight(.semibold)
                        .accessibilityAddTraits(.isHeader)
                }
                SettingsTabView(model: model, section: tab.section)
            }
            .padding(.horizontal, 24)
            .padding(.vertical, 20)
            .frame(maxWidth: .infinity, alignment: .leading)
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
        // The sidebar and the page run up under a clear title bar (no grey strip above them);
        // the page header names the page, so the window title is hidden.
        window.styleMask.insert(.fullSizeContentView)
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.collectionBehavior.insert([.moveToActiveSpace, .fullScreenAuxiliary])
        // First open: the window exists only now; bring it in front of other apps' windows.
        WindowFocus.bringForward(window, app: NSApp, showInDock: true)
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
                HStack(spacing: 6) {
                    // Never shows a stored key: the field starts empty and clears after a save.
                    SecureField("Paste key", text: Binding(get: { model.keyDraft(id) },
                                                           set: { model.setKeyDraft(id, $0) }))
                        .frame(width: 220)
                        .onSubmit { model.saveKey(id) }
                        .accessibilityLabel(label)
                        .accessibilityHint(help)
                    Button("Save") { model.saveKey(id) }
                        .disabled(!model.canSaveKey(id))
                        .help("Keeps this key in the Keychain.")
                    if model.hasStoredKey(id) {
                        Button("Remove") { model.removeKey(id) }
                            .help("Deletes the key kept in the Keychain.")
                    }
                }
            } note: {
                VStack(alignment: .leading, spacing: 2) {
                    if let error = model.keyError(id) {
                        NoteText(text: error, isError: true)
                    }
                    Text(model.secretText(id))
                        .font(.callout)
                        .accessibilityLabel("\(label): \(model.secretText(id))")
                    NoteText(text: help, isError: false)
                }
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
