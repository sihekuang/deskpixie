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
        // The system's sidebar and page (as System Settings and Finder): its materials, title
        // bar and search field, nothing drawn by hand (#391). The field belongs to the split
        // view, not the list, so the list turning into results keeps its focus (#374).
        NavigationSplitView {
            sidebar
                .navigationSplitViewColumnWidth(min: 200, ideal: 220, max: 280)
                .toolbar(removing: .sidebarToggle)
        } detail: {
            SettingsDetailView(model: model, tab: SettingsLayout.tabs[selectedIndex])
        }
        .searchable(text: Binding(get: { model.query }, set: { model.query = $0 }),
                    placement: .sidebar, prompt: "Search")
        .background(Button("") { observers.focusSearch() }.keyboardShortcut("f").opacity(0).accessibilityHidden(true))
        .frame(minWidth: 720, minHeight: 520)
        .background(WindowAccessor { window in observers.attach(window, model: model) })
        .onAppear {
            // .regular lets the window become key and show in ⌘-Tab; back to .accessory
            // (menu-bar only) when it closes (Howl's AppDelegate.showSettingsWindow).
            NSApp.setActivationPolicy(.regular)
            NSApp.activate()
            // A reveal made before the window was built (Pixie's open_settings) picks the page.
            if let section = model.pendingRevealSection, let index = SettingsLayout.tabIndex(section) {
                tab = index
            }
            model.load()
        }
        // A picked search result, or a setting Pixie was asked to show (#288): go to its page;
        // the page scrolls to the row and points it out.
        .onChange(of: model.revealCount) {
            guard let hit = model.revealed, let index = SettingsLayout.tabIndex(hit.section) else { return }
            tab = index
        }
    }

    /// A sidebar row: a page, or a setting the search found.
    private enum SidebarItem: Hashable {
        case page(Int)
        case hit(String)
    }

    /// The pages, or while searching, what the search found (#287). One list whose rows change,
    /// not two lists: the search field belongs to the sidebar and is built again with a new
    /// list, losing its focus after the first character (#374).
    private var sidebar: some View {
        List(selection: Binding<SidebarItem?>(
            get: {
                if model.searching { return model.revealed.map { .hit($0.path) } }
                return .page(selectedIndex)
            },
            set: { item in
                switch item {
                case .hit(let path):
                    if let hit = model.hits.first(where: { $0.path == path }) { model.reveal(hit) }
                case .page(let new) where new != selectedIndex:
                    tab = new
                    model.recordingPath = nil
                default:
                    break
                }
            }
        )) {
            if model.searching {
                if model.hits.isEmpty {
                    Text(SettingsLayout.noResultsText(model.query))
                        .foregroundStyle(.secondary)
                        .selectionDisabled()
                }
                ForEach(model.hits, id: \.path) { hit in
                    SettingsHitRow(hit: hit).tag(SidebarItem.hit(hit.path))
                }
            } else {
                ForEach(Array(SettingsLayout.tabs.enumerated()), id: \.offset) { index, t in
                    SettingsSidebarRow(tab: t).tag(SidebarItem.page(index))
                }
            }
        }
        .listStyle(.sidebar)
    }
}

/// One search result: the setting's name, and the page it is on.
private struct SettingsHitRow: View {
    let hit: SettingsHit

    var body: some View {
        VStack(alignment: .leading, spacing: 1) {
            Text(hit.label)
            Text(SettingsLayout.place(hit))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .help(hit.help)
        .accessibilityElement(children: .combine)
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

/// The selected page: the tab's rows, in a form that scrolls when taller than the window.
private struct SettingsDetailView: View {
    let model: SettingsModel
    let tab: SettingsLayout.Tab

    /// The row being pointed out, for a moment after it was revealed.
    @State private var highlighted: String?

    var body: some View {
        ScrollViewReader { proxy in
            SettingsTabView(model: model, section: tab.section, highlighted: highlighted)
            // The page's name is the window's title, as in System Settings (#391).
            .navigationTitle(tab.title)
            // Runs again when the page changes, since a reveal can switch to another page first.
            .task(id: "\(model.revealCount)|\(tab.section.rawValue)") {
                guard let hit = model.takeReveal(for: tab.section) else { return }
                // The page may have only just been switched to: let its rows lay out first.
                do {
                    try await Task.sleep(for: .milliseconds(50))
                } catch {
                    return
                }
                withAnimation { proxy.scrollTo(hit.path, anchor: .center) }
                highlighted = hit.path
                do {
                    // A window just brought forward (Pixie's open_settings, #288) reloads and lays
                    // the page out again, losing the first scroll: scroll once more.
                    try await Task.sleep(for: .milliseconds(400))
                    withAnimation { proxy.scrollTo(hit.path, anchor: .center) }
                    try await Task.sleep(for: .milliseconds(1600))
                } catch {
                    highlighted = nil
                    return
                }
                withAnimation(.easeOut(duration: 0.6)) { highlighted = nil }
            }
        }
    }
}

/// Watches the hosting window: becoming key reloads (hand edits, a key added to `.env`);
/// closing commits typed text, stops recording and hides the dock icon again.
@MainActor
private final class WindowObservers {
    private var tokens: [NSObjectProtocol] = []
    private weak var window: NSWindow?

    /// ⌘F: into the search field (#287), which the system puts in the window's title bar.
    func focusSearch() {
        guard let window, let frame = window.contentView?.superview,
              let field = Self.searchField(in: frame) else { return }
        window.makeFirstResponder(field)
    }

    private static func searchField(in view: NSView) -> NSSearchField? {
        if let field = view as? NSSearchField { return field }
        return view.subviews.lazy.compactMap(searchField).first
    }

    /// The sidebar stays open, as in System Settings: there is no toggle to bring it back
    /// (#391). The split view exists only once the window has laid out.
    private static func keepSidebar(in window: NSWindow) {
        guard let split = window.contentView.flatMap(splitView),
              let controller = split.delegate as? NSSplitViewController else { return }
        controller.splitViewItems.first?.canCollapse = false
    }

    private static func splitView(in view: NSView) -> NSSplitView? {
        if let split = view as? NSSplitView { return split }
        return view.subviews.lazy.compactMap(splitView).first
    }

    func attach(_ window: NSWindow, model: SettingsModel) {
        guard self.window !== window else { return }
        tokens.forEach(NotificationCenter.default.removeObserver)
        self.window = window
        window.identifier = NSUserInterfaceItemIdentifier("settings")
        window.setFrameAutosaveName(SettingsWindowView.frameName)
        window.level = .normal
        window.collectionBehavior.insert([.moveToActiveSpace, .fullScreenAuxiliary])
        DispatchQueue.main.async { Self.keepSidebar(in: window) }
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
                    AppDock.windowClosed("settings")
                }
            },
        ]
    }
}

/// One tab, as a grouped form: the file-error banner, a section of name / note / control rows
/// per group (the extra rows among them), the warning (or notice) line and the Restart bar.
struct SettingsTabView: View {
    let model: SettingsModel
    /// The pix command row (#89); the login shell's PATH is read once, when Settings opens.
    @State private var pix = PixCommandModel(installer: .forThisApp(path: PixInstaller.loginShellPath()))
    /// Restart Coordinator… was clicked: ask first (#179).
    @State private var confirmRestart = false
    let section: SettingsSection
    /// The row a search or Pixie pointed out (a field path or an extra's `path`).
    var highlighted: String? = nil

    var body: some View {
        // Grouped rounded sections, as in System Settings (#393).
        Form {
            if let banner = model.bannerText {
                Section {
                    SettingsStrip(symbol: "exclamationmark.triangle.fill", text: banner, buttonTitle: "Open File",
                                  buttonHelp: "Shows settings.json in Finder.") { model.openFile() }
                }
            }
            if model.snapshot == nil {
                Section {
                    Text(model.loadError.map { "Couldn't read settings: \($0)" } ?? "Loading…")
                        .foregroundStyle(.secondary)
                }
            } else {
                let groups = model.groups(in: section)
                ForEach(groups, id: \.title) { group in
                    Section {
                        ForEach(group.rows, id: \.id) { row in
                            switch row {
                            case .field(let field): fieldRow(field)
                            case .extra(let extra): extraRow(extra)
                            }
                        }
                    } header: {
                        // A page of one group needs no heading: the page's title names it.
                        if groups.count > 1 {
                            Text(group.title)
                        }
                    }
                }
            }
            if let notice = model.noticeText {
                Section {
                    NoteText(text: notice, isError: true)
                        .accessibilityAddTraits(.isStaticText)
                }
            }
            if let restart = model.restartText {
                Section {
                    SettingsStrip(symbol: nil, text: restart, buttonTitle: "Restart",
                                  buttonHelp: "Restarts DeskPixie to apply the changes.") { model.restart() }
                }
            }
        }
        .formStyle(.grouped)
        .alert(CoordinatorRestart.title, isPresented: $confirmRestart) {
            Button(CoordinatorRestart.confirm, role: .destructive) { model.restartCoordinator() }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(CoordinatorRestart.message)
        }
    }

    /// A setting: its label, its control (and unit), and its note; greyed out while the switch
    /// it depends on is off, with a line saying which.
    @ViewBuilder
    private func fieldRow(_ field: SettingsField) -> some View {
        let spec = model.spec(field)
        let canChange = model.canChange(field)
        row(label: spec.label, id: field.path) {
            HStack(spacing: 6) {
                SettingsControlView(model: model, spec: spec)
                if let unit = field.unit {
                    Text(unit).foregroundStyle(.secondary)
                }
            }
            .disabled(!canChange)
        } note: {
            VStack(alignment: .leading, spacing: 2) {
                if let off = model.disabledNote(field) {
                    NoteText(text: off, isError: false)
                }
                NoteText(text: spec.note.text, isError: spec.note.isError)
            }
            .opacity(canChange ? 1 : 0.6)
        }
        if field.path == SettingsModel.projectsRootPath, canChange, let folder = model.projectsFolder {
            projectsFolderRow(folder)
        }
    }

    /// The row under the projects folder: whether it is there, with Create when it is missing (#216).
    private func projectsFolderRow(_ folder: ProjectsFolderStatus) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Label {
                Text(folder.state == .ready ? "Found" : folder.detail)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: 340, alignment: .leading)
            } icon: {
                Image(systemName: folder.state == .ready ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                    .foregroundStyle(folder.state == .ready ? Color.green : Color.orange)
                    .accessibilityHidden(true)
            }
            HStack(spacing: 8) {
                if folder.state == .missing {
                    Button("Create") { model.createProjectsFolder() }
                        .disabled(!model.enabled)
                }
                Button("Check Again") { model.refreshProjectsFolder() }
            }
            .controlSize(.small)
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Projects folder: \(folder.state == .ready ? "found" : folder.detail)")
    }

    /// Export Logs… (#225): where to save, then the core writes the zip; it is shown in Finder.
    private func exportLogs() {
        let panel = NSSavePanel()
        panel.title = "Export Logs"
        panel.nameFieldStringValue = SettingsLayout.logExportName(Date())
        panel.allowedContentTypes = [.zip]
        panel.directoryURL = FileManager.default.urls(for: .desktopDirectory, in: .userDomainMask).first
        guard panel.runModal() == .OK, let url = panel.url else { return }
        let v = ProcessInfo.processInfo.operatingSystemVersion
        let os = "macOS \(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
        if model.exportLogs(to: url.path, app: SystemPermissions.appVersion, os: os) {
            NSWorkspace.shared.activateFileViewerSelecting([url])
        }
    }

    @ViewBuilder
    private func extraRow(_ extra: SettingsLayout.Extra) -> some View {
        switch extra {
        case .button(let label, let help, let title, let action):
            row(label: label, id: extra.path) {
                Button(title) {
                    switch action {
                    case .resetPosition: model.resetPosition()
                    case .openFile: model.openFile()
                    case .restartCoordinator: confirmRestart = true
                    case .exportLogs: exportLogs()
                    }
                }
                .disabled(!model.enabled)
                .help(help)
            } note: {
                NoteText(text: help, isError: false)
            }
        case .pix(let help):
            row(label: extra.label, id: extra.path) {
                HStack(spacing: 6) {
                    switch pix.state {
                    case .installed:
                        Button("Uninstall") { pix.uninstall() }
                            .help("Removes ~/.local/bin/pix.")
                    case .notInstalled, .broken:
                        Button("Install") { pix.install() }
                            .help("Links ~/.local/bin/pix to the pix inside DeskPixie, so it updates with the app.")
                    case .other:
                        EmptyView()
                    }
                    Button("Copy Aliases") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(PixInstaller.aliases, forType: .string)
                    }
                    .help("Copies alias lines for ~/.zshrc, so plain `claude` and `codex` run through pix.")
                }
                .onAppear { pix.refresh() }
            } note: {
                VStack(alignment: .leading, spacing: 2) {
                    Text(pix.stateText)
                        .font(.subheadline)
                        .accessibilityLabel("pix command: \(pix.stateText)")
                    if let error = pix.error {
                        NoteText(text: error, isError: true)
                    }
                    if !pix.onPath {
                        NoteText(text: "~/.local/bin is not on your PATH. Add to ~/.zshrc: \(PixInstaller.pathLine)", isError: false)
                    }
                    NoteText(text: help, isError: false)
                    Link("How pix works", destination: URL(string: "https://github.com/sihekuang/deskpixie/blob/main/docs/pix.md")!)
                        .font(.subheadline)
                }
            }
        case .secret(let id, let label, let help):
            row(label: label, id: extra.path) {
                HStack(spacing: 6) {
                    // Never shows a stored key: the field starts empty and clears after a save.
                    SecureField(label, text: Binding(get: { model.keyDraft(id) },
                                                     set: { model.setKeyDraft(id, $0) }),
                                prompt: Text("Paste key"))
                        .labelsHidden()
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 160)
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
                        .font(.subheadline)
                        .accessibilityLabel("\(label): \(model.secretText(id))")
                    NoteText(text: help, isError: false)
                }
            }
        }
    }

    /// One setting's row: its name with the note under it, and its control on the right.
    private func row<Control: View, Note: View>(label: String, id: String, @ViewBuilder control: () -> Control,
                                                @ViewBuilder note: () -> Note) -> some View {
        LabeledContent {
            control()
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                Text(label)
                    // Pointed out by a search or Pixie: a tint just around the name.
                    .background(
                        RoundedRectangle(cornerRadius: 4, style: .continuous)
                            .fill(Color.accentColor.opacity(highlighted == id ? 0.3 : 0))
                            .padding(.horizontal, -4)
                    )
                    .accessibilityHidden(true)
                note()
            }
        }
        .id(id)
    }
}
