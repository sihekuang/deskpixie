import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The chat window (#302): an ordinary titled window, "Pixie", its size and place remembered,
/// with a Dock icon while it is open, like Settings. It never opens by itself: `show(at:)` is
/// the only way in (the menu bar's Chat…, the button above Pixie, ⌘⇧C). The button and ⌘⇧C
/// also `hide()` it, which keeps the window and where it was scrolled to; its red button and
/// ⌘W close it. Clear History… sits in its toolbar menu.
@MainActor
final class ChatWindowController: NSObject, NSToolbarDelegate {
    static let frameName = "DeskPixieChatWindow"
    static let windowID = "chat"
    static let minSize = NSSize(width: 400, height: 480)
    static let defaultSize = NSSize(width: 720, height: 760)

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "chat")
    nonisolated private static let menuItemID = NSToolbarItem.Identifier("chat.menu")

    private let model: ChatThreadModel
    private let engine: CoreEngine
    private let worker: CoreWorker
    /// The attachments shared with the box under Pixie; nil until Pixie is up.
    private let attachments: () -> AttachmentUI?
    private let state = ChatWindowState()
    /// Shown or not, for the button above Pixie.
    private let presence: ChatWindowPresence
    private let richText: RichTextStore
    private var window: NSWindow?
    private var tokens: [NSObjectProtocol] = []
    private var pasteMonitor: Any?

    init(model: ChatThreadModel, engine: CoreEngine, worker: CoreWorker, presence: ChatWindowPresence,
         attachments: @escaping () -> AttachmentUI?) {
        self.model = model
        self.presence = presence
        self.engine = engine
        self.worker = worker
        self.attachments = attachments
        richText = RichTextStore(engine: engine)
    }

    /// Opens the window (or brings it forward), at this entry or, with none, at the oldest
    /// unread, else the newest. Shown again after `hide()` (or from under other windows) with
    /// nothing new, it stays where it was; with something new, it goes to the oldest unread
    /// before any of it counts as read. The thread is loaded again only on a real open.
    func show(at entryID: String? = nil) {
        let opening = self.window == nil
        let window = self.window ?? makeWindow()
        let plan = ChatShow(opening: opening, onScreen: !opening && Self.onScreen(window),
                            entryID: entryID, unread: model.unread)
        // Before the window is visible again, so nothing is read until the scroll has run.
        if plan.reopens { state.opening() }
        WindowFocus.bringForward(window, app: NSApp, showInDock: true)
        updateVisible()
        state.focusRequests += 1
        guard plan.scrolls else { return }
        guard plan.reloads else { return state.scroll(to: entryID) }
        let model = model, state = state
        Task { @MainActor in
            await model.load()
            state.scroll(to: entryID)
        }
    }

    /// On screen: shown, not minimized, and not covered by other windows or on another Space.
    private static func onScreen(_ window: NSWindow) -> Bool {
        ChatToggle.isOnScreen(visible: window.isVisible, unoccluded: window.occlusionState.contains(.visible),
                              miniaturized: window.isMiniaturized)
    }

    /// Puts the window away, keeping it (its size, place and scroll) for the next `show`.
    func hide() {
        guard let window, window.isVisible else { return }
        window.orderOut(nil)
        updateVisible()
        AppDock.windowClosed(Self.windowID)
    }

    /// Every message's links are found again (a setting changed where files open).
    func settingsChanged() { richText.reset() }

    private func makeWindow() -> NSWindow {
        let actions = ChatActions(
            focusSession: { [weak self] in self?.focusSession($0) },
            focus: { [weak self] in self?.focus($0) },
            decide: { [model] token, allow in Task { await model.decide(token: token, allow: allow) } },
            choose: { [model] token, option in Task { await model.choose(token: token, option: option) } },
            reply: { [weak self] entry in
                self?.model.reply(to: entry)
                self?.state.focusRequests += 1
            },
            stopTarget: { [model] in model.stopTarget(for: $0) },
            liveWho: { [model] in model.liveWho($0) },
            stop: { [model] target in Task { await model.stop(target) } })
        let view = ChatWindowView(model: model, state: state, attachments: attachments, actions: actions)
            .environmentObject(richText)
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: Self.defaultSize),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: false)
        window.title = "Pixie"
        window.identifier = NSUserInterfaceItemIdentifier(Self.windowID)
        window.setAccessibilityIdentifier("chat.window")
        window.isReleasedWhenClosed = false
        window.contentMinSize = Self.minSize
        window.contentView = NSHostingView(rootView: view)
        window.collectionBehavior.insert([.moveToActiveSpace, .fullScreenAuxiliary])
        let toolbar = NSToolbar(identifier: "chat.toolbar")
        toolbar.delegate = self
        toolbar.displayMode = .iconOnly
        window.toolbar = toolbar
        window.toolbarStyle = .unifiedCompact
        // A remembered frame wins; the first time, the default size in the middle of the screen.
        window.center()
        window.setFrameAutosaveName(Self.frameName)
        let center = NotificationCenter.default
        tokens = [
            center.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.closed() }
            },
            // Back to the window: the cursor goes back in the box.
            center.addObserver(forName: NSWindow.didBecomeKeyNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.state.focusRequests += 1 }
            },
        ] + [NSWindow.didChangeOcclusionStateNotification, NSWindow.didMiniaturizeNotification,
             NSWindow.didDeminiaturizeNotification].map { name in
            // On screen or not: what the thread shows counts as read only while it is.
            center.addObserver(forName: name, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.updateVisible() }
            }
        }
        if pasteMonitor == nil {
            // Cmd-V in the box with only an image or files on the clipboard attaches them, as in
            // the box under Pixie; text pastes as usual.
            pasteMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
                let isPaste = ClipboardRules.isPasteShortcut(flags: event.modifierFlags.rawValue,
                                                             key: event.charactersIgnoringModifiers)
                guard isPaste else { return event }
                let took = MainActor.assumeIsolated { self?.pasteAttaches(event) ?? false }
                return took ? nil : event
            }
        }
        self.window = window
        return window
    }

    /// Only while the box is being typed in (an editable text view of this window has the keys).
    private func pasteAttaches(_ event: NSEvent) -> Bool {
        guard let window, event.window === window, window.isKeyWindow,
              let editor = window.firstResponder as? NSTextView, editor.isEditable,
              let ui = attachments() else { return false }
        return ui.onPaste()
    }

    /// The button and the read rule use the same test: a covered window reads "Show chat".
    private func updateVisible() {
        let shown = window.map(Self.onScreen) ?? false
        presence.shown = shown
        state.visible = shown
    }

    private func closed() {
        tokens.forEach(NotificationCenter.default.removeObserver)
        tokens = []
        if let m = pasteMonitor {
            NSEvent.removeMonitor(m)
            pasteMonitor = nil
        }
        window = nil
        state.visible = false
        presence.shown = false
        model.cancelReply()
        AppDock.windowClosed(Self.windowID)
    }

    /// Asks first; then `chat.clear`, and the core's `chat.reset` empties the thread.
    @objc private func clearHistory(_ sender: Any?) {
        guard let window else { return }
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Clear the chat?"
        alert.informativeText = "This removes everything it kept on this Mac."
        alert.addButton(withTitle: "Clear").hasDestructiveAction = true
        alert.addButton(withTitle: "Cancel").keyEquivalent = "\u{1b}"
        let model = model
        alert.beginSheetModal(for: window) { response in
            guard response == .alertFirstButtonReturn else { return }
            Task { @MainActor in await model.clear() }
        }
    }

    private func focusSession(_ name: String) {
        struct Args: Encodable, Sendable { let name: String }
        let engine = engine
        worker.async {
            do {
                _ = try engine.call("agent.focusSession", Args(name: name))
            } catch {
                Self.log.error("agent.focusSession: \(String(describing: error), privacy: .public)")
            }
        }
    }

    /// The terminal button by a name: the call the package picked (`terminalTarget(for:)`).
    private func focus(_ target: ChatTerminalTarget) {
        struct Name: Encodable, Sendable { let name: String }
        struct Job: Encodable, Sendable { let job: String; let worker: String }
        let engine = engine
        worker.async {
            do {
                switch target.call {
                case .focusSession, .focusAgent:
                    _ = try engine.call(target.call.rawValue, Name(name: target.name))
                case .focusJob:
                    _ = try engine.call(target.call.rawValue, Job(job: target.job, worker: target.name))
                }
            } catch {
                Self.log.error("\(target.call.rawValue, privacy: .public): \(String(describing: error), privacy: .public)")
            }
        }
    }

    // MARK: NSToolbarDelegate

    nonisolated func toolbarDefaultItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.flexibleSpace, Self.menuItemID]
    }

    nonisolated func toolbarAllowedItemIdentifiers(_ toolbar: NSToolbar) -> [NSToolbarItem.Identifier] {
        [.flexibleSpace, Self.menuItemID]
    }

    nonisolated func toolbar(_ toolbar: NSToolbar, itemForItemIdentifier id: NSToolbarItem.Identifier,
                             willBeInsertedIntoToolbar flag: Bool) -> NSToolbarItem? {
        MainActor.assumeIsolated {
            guard id == Self.menuItemID else { return nil }
            let menu = NSMenu()
            let clear = NSMenuItem(title: "Clear History…", action: #selector(clearHistory(_:)), keyEquivalent: "")
            clear.target = self
            clear.setAccessibilityIdentifier("chat.clearHistory")
            menu.addItem(clear)
            let item = NSMenuToolbarItem(itemIdentifier: id)
            item.menu = menu
            item.image = NSImage(systemSymbolName: "ellipsis.circle", accessibilityDescription: "More")
            item.label = "More"
            item.toolTip = "More"
            item.showsIndicator = false
            return item
        }
    }
}
