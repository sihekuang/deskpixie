import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The roster chip under Pixie (#91): a child panel of the pixie panel showing
/// `AgentRosterModel`. Hovering or clicking the chip opens the list; a row brings its agent
/// forward (`agent.focusAgent`), or your session's own tab (`agent.focusSession`). Opaque
/// through Pixie's proximity fade; hidden with Pixie. The pointer is polled, like the updates bubble.
@MainActor
final class AgentRosterController {
    static let pollInterval: TimeInterval = 0.1

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "roster")

    private let engine: CoreEngine
    private let worker: CoreWorker
    private let pixie: PixieController
    private let panel: PixiePanel
    private let hosting: RosterHostingView
    private(set) var model = AgentRosterModel()
    private var below = true
    private var timer: Timer?
    private var eventsApplied = 0
    /// The chip alone, measured while closed: the open height must not depend on the phase, or
    /// the side flips when the list opens (and flickers against the poll).
    private var chipHeight: CGFloat = 22
    /// The text box (#221): its model, what survives redraws, the toggle's place in the window
    /// (hovering it doesn't open the list), the word-by-word timer and the click-away monitor.
    private(set) var chat = QuickChatModel()
    private let chatState = QuickChatState()
    /// Where the session chip is in the window (top-left origin): only it opens the list on hover.
    private var chipFrame: CGRect = .zero
    /// Captions (#224): kept up always (the setting), the pointer near Pixie, and the "ago"
    /// last drawn.
    private var alwaysCaption = false
    /// captionDismissSecs: the words are put away this long after they were said (0 keeps them).
    private var dismissSecs = 120
    private var hovering = false
    private var agoShown: String?
    /// How near Pixie, in points, the pointer brings the last words back.
    static let hoverMargin: CGFloat = 24
    private var typeTimer: Timer?
    private var clickAway: Any?
    static let typeInterval: TimeInterval = 0.12
    /// Captions type at about speaking pace (#224); they stay this long after the conversation.
    static let captionInterval: TimeInterval = 0.3
    static let captionLinger: TimeInterval = 3
    private var captionHide: Timer?
    /// The attachments waiting for the next request (#240), and Cmd-V while the box is open.
    private var attach: AttachmentsController!
    private var pasteMonitor: Any?
    /// Pixie's "Want me to take this?" for each new copy of an image or files.
    private var watch = ClipboardWatch()
    private var offer = CopyOffer()
    private var watchTicks = 0

    init(engine: CoreEngine, worker: CoreWorker, pixie: PixieController) {
        self.engine = engine
        self.worker = worker
        self.pixie = pixie
        panel = PixiePanel(frame: NSRect(x: 0, y: 0, width: 1, height: 1))
        hosting = RosterHostingView(rootView: AnyView(EmptyView()))
        panel.contentView = hosting
        pixie.observeFrame { [weak self] _ in self?.layout() }
        timer = Timer.scheduledTimer(withTimeInterval: Self.pollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.poll() }
        }
        RunLoop.main.add(timer!, forMode: .common)
        attach = AttachmentsController(engine: engine, worker: worker, clipboard: PasteboardClipboard()) { [weak self] in
            self?.render()
        }
        pixie.view.onDrop = { [weak self] board in self?.attach.drop(board: board) ?? false }
        fetchCurrent()
        fetchMode()
    }

    // MARK: - attachments (#240)

    /// The core's `attachments` event.
    func apply(attachments items: [AttachmentItem]) { attach.apply(items) }

    /// Pixie's take_clipboard (the `clipboard.read` event).
    func takeClipboard(request: String) { attach.takeClipboard(request: request) }

    var attachments: AttachmentsController { attach }

    /// A click on Pixie's offer: what was copied, attached.
    private func takeOffer() {
        offer.take()
        attach.attachClipboard()
        // Text mode: the box opens with it; voice mode keeps the card under Pixie (#221's rule).
        if chat.mode == "text" && !chat.open { openChat() } else { render() }
    }

    /// Every half second: a new copy brings the offer up.
    private func watchClipboard(hovering: Bool) -> Bool {
        watchTicks += 1
        var changed = offer.expire(now: Date(), hovering: hovering)
        guard watchTicks % 5 == 0 else { return changed }
        attach.poll()
        if watch.poll(changeCount: attach.clipboard.changeCount, kind: attach.clipboardKind) {
            offer.show(attach.clipboardKind, now: Date())
            changed = true
        }
        return changed
    }

    /// Cmd-V in the open box: attaches when the clipboard holds only an image or files.
    private func pasteAttaches() -> Bool {
        guard chat.open, panel.isKeyWindow, ClipboardRules.pasteAttaches(attach.clipboard.detectedTypes()) else { return false }
        offer.take()
        attach.attachClipboard()
        return true
    }

    private var attachmentUI: AttachmentUI {
        let a = attach!
        return AttachmentUI(model: a.model, hint: a.hint,
                            onAttachHint: { a.attachClipboard() },
                            onRemove: { a.remove($0) },
                            onPick: { a.pick() },
                            onDrop: { a.drop($0) })
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        typeTimer?.invalidate()
        typeTimer = nil
        captionHide?.invalidate()
        captionHide = nil
        closeChat()
        hide()
    }

    // MARK: - the text box (#221)

    /// replyMode changed (the `replyMode` event or the first read).
    func apply(mode: String) {
        chat.mode = mode
        if mode != "text" { closeChat() }
        render()
    }

    /// The always-show switch and the put-away time (the `captions` event or the first read, #224).
    func apply(captions always: Bool, dismissSecs: Int) {
        alwaysCaption = always
        self.dismissSecs = dismissSecs
        render()
    }

    /// The ✕ on the card: the words go at once.
    private func dismissWords() {
        chat.dismiss()
        typeTimer?.invalidate()
        typeTimer = nil
        render()
    }

    /// A spoken line for the typed message.
    func apply(chatLine: ChatLine) {
        chat.apply(chatLine)
        if chat.phase == .answering { startTyping() }
        render()
    }

    /// What Pixie said aloud (#224): a caption where the text box goes (voice mode: on its own).
    func caption(role: String, text: String) {
        guard chat.caption(role: role, text: text) else { return }
        captionHide?.invalidate()
        captionHide = nil
        typeTimer?.invalidate()
        typeTimer = nil
        startTyping()
        render()
    }

    /// The voice conversation opened or ended (the mic): an ended one's caption goes a moment later.
    func conversation(open: Bool) {
        captionHide?.invalidate()
        captionHide = nil
        guard !open else { return }
        captionHide = Timer.scheduledTimer(withTimeInterval: Self.captionLinger, repeats: false) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.captionHide = nil
                self.chat.conversationEnded()
                self.render()
            }
        }
    }

    /// The hotkey in text mode: open the box and put the cursor in it.
    func focusChat() {
        guard chat.mode == "text" else { return }
        openChat()
    }

    private func fetchMode() {
        let engine = engine, worker = worker
        Task { @MainActor [weak self] in
            let reply: ModeReply?? = await worker.run { try? engine.call("chat.mode", as: ModeReply.self) }
            guard let self, let reply = reply ?? nil else { return }
            self.alwaysCaption = reply.alwaysShowCaption ?? false
            self.dismissSecs = reply.captionDismissSecs ?? 120
            self.apply(mode: reply.mode)
        }
    }

    private func pick(_ mode: String) {
        if mode == chat.mode {
            // 💬 again toggles the box; 🔊 again does nothing.
            if mode == "text" { chat.open ? closeChat() : openChat() }
            return
        }
        apply(mode: mode) // at once; the core's replyMode event confirms it
        if mode == "text" { openChat() }
        let engine = engine
        worker.async {
            do {
                _ = try EngineSettingsClient(engine: engine).set("replyMode", .string(mode))
            } catch {
                Self.log.error("replyMode: \(String(describing: error), privacy: .public)")
            }
        }
    }

    private func openChat() {
        model.open = false // never both
        chat.open = true
        panel.acceptsKey = true
        render()
        panel.makeKey()
        chatState.focusRequests += 1
        attach.poll()
        if pasteMonitor == nil {
            // Cmd-V with only an image or files on the clipboard attaches them; text pastes.
            pasteMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
                let isPaste = ClipboardRules.isPasteShortcut(flags: event.modifierFlags.rawValue,
                                                             key: event.charactersIgnoringModifiers)
                guard isPaste else { return event }
                let took = MainActor.assumeIsolated { self?.pasteAttaches() ?? false }
                return took ? nil : event
            }
        }
        if clickAway == nil {
            // A click in another app puts the box away (clicks on Pixie's own panels are local).
            clickAway = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
                MainActor.assumeIsolated { self?.closeChat() }
            }
        }
    }

    private func closeChat() {
        if let m = clickAway {
            NSEvent.removeMonitor(m)
            clickAway = nil
        }
        if let m = pasteMonitor {
            NSEvent.removeMonitor(m)
            pasteMonitor = nil
        }
        guard chat.open else { return }
        chat.open = false
        panel.acceptsKey = false
        if panel.isKeyWindow { panel.resignKey() }
        render()
    }

    private func send() {
        let text = chatState.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard chat.canSend(text, attachments: attach.model.items.count) else { return }
        chatState.draft = ""
        attach.sent()
        chat.sending()
        render()
        let engine = engine, worker = worker
        Task { @MainActor [weak self] in
            let reply: Result<String, Error>? = await worker.run {
                Result { try engine.call("chat.send", SendArgs(text: text), as: SendReply.self).job }
            }
            guard let self else { return }
            let result = reply ?? .failure(DeskPixieEngineError.callFailed("Pixie couldn't send that."))
            switch result {
            case .success(let job):
                // Stop came before the job was known: cancel it now.
                if self.chat.sent(job: job) { self.cancelInCore() }
            case .failure(let error): self.chat.failedToSend(Self.reason(error))
            }
            self.render()
        }
    }

    private func stopChat() {
        // Only thinking means the coordinator is at work; typing out just finishes at once.
        let cancel = chat.stopped()
        typeTimer?.invalidate()
        typeTimer = nil
        render()
        if cancel { cancelInCore() }
    }

    private func cancelInCore() {
        let engine = engine
        worker.async { _ = try? engine.call("chat.stop") }
    }

    private func startTyping() {
        guard typeTimer == nil else { return }
        let pace = chat.isCaption ? Self.captionInterval : Self.typeInterval
        typeTimer = Timer.scheduledTimer(withTimeInterval: pace, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                if !self.chat.tick() {
                    self.typeTimer?.invalidate()
                    self.typeTimer = nil
                }
                self.render()
            }
        }
        RunLoop.main.add(typeTimer!, forMode: .common)
    }

    /// The core's error text as the user should read it.
    private static func reason(_ error: Error) -> String {
        if case DeskPixieEngineError.callFailed(let message) = error { return message }
        return "Pixie couldn't send that."
    }

    /// The core's roster (`agents` event).
    func apply(view: RosterView) {
        eventsApplied += 1
        model.apply(view)
        render()
    }

    private func fetchCurrent() {
        let engine = engine, worker = worker, since = eventsApplied
        Task { @MainActor [weak self] in
            let view: RosterView?? = await worker.run {
                do {
                    return try engine.call("agent.roster", as: RosterReply.self).roster
                } catch {
                    Self.log.error("agent.roster: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            guard let self, let view = view ?? nil, self.eventsApplied == since else { return }
            self.apply(view: view)
        }
    }

    private func clicked(_ r: RosterRow) {
        guard AgentRosterModel.clickable(r) else { return }
        // Your own session: the core finds the tab it runs in (#148); Pixie's agents: their pane.
        let method = r.kind == "session" ? "agent.focusSession" : "agent.focusAgent"
        let engine = engine, name = r.name
        worker.async {
            do {
                _ = try engine.call(method, FocusAgentArgs(name: name))
            } catch {
                Self.log.error("\(method, privacy: .public): \(String(describing: error), privacy: .public)")
            }
        }
    }

    private func poll() {
        let mouse = NSEvent.mouseLocation
        let overPanel = panel.isVisible && panel.frame.contains(mouse)
        let offerChanged = watchClipboard(hovering: overPanel)
        // The list opens over the chip and closes when the pointer leaves the chip and the list;
        // never while the text box is open.
        var inside = false
        if overPanel && model.visible && !chat.open {
            let p = panel.convertPoint(fromScreen: mouse)
            let flipped = CGPoint(x: p.x, y: hosting.bounds.height - p.y)
            inside = model.open || chipFrame.insetBy(dx: -3, dy: -3).contains(flipped)
        }
        // Near Pixie, or on the card it brought back: the last words show (#224).
        let near = pixie.isShown
            && (pixie.panel.frame.insetBy(dx: -Self.hoverMargin, dy: -Self.hoverMargin).contains(mouse)
                || (overPanel && captionCard))
        // Put away once old enough (captionDismissSecs).
        let expired = chat.dismissIfOlder(than: dismissSecs, now: Date())
        let ago = cardUp ? chat.ago(now: Date()) : nil
        if offerChanged || expired || inside != model.open || near != hovering || ago != agoShown {
            model.open = inside
            hovering = near
            render()
        }
    }

    /// The card without the input line: a live caption, or the last words on hover or always;
    /// never with the list or the open text box.
    private var captionCard: Bool {
        !chat.open && !model.open
            && (chat.showsCard(hovering: hovering, always: alwaysCaption) || attach.model.chipsVisible)
    }

    private func render() {
        setRoot()
        layout()
    }

    private func setRoot() {
        agoShown = cardUp ? chat.ago(now: Date()) : nil
        hosting.rootView = AnyView(AgentRosterView(
            model: model, below: below, maxChipWidth: max(40, pixie.panel.frame.width),
            onChip: { [weak self] in
                // Hover already opens the list; a click only opens it (never closes, so click and
                // hover poll can't fight). Leaving the panel closes it.
                guard let self, !self.model.open else { return }
                self.closeChat()
                self.model.open = true
                self.render()
            },
            onRow: { [weak self] in self?.clicked($0) },
            chat: chat, chatState: chatState, captionCard: captionCard, ago: agoShown,
            onMode: { [weak self] in self?.pick($0) },
            onSend: { [weak self] in self?.send() },
            onStop: { [weak self] in self?.stopChat() },
            onCloseChat: { [weak self] in self?.closeChat() },
            onResize: { [weak self] in
                // After SwiftUI has laid the new lines out.
                DispatchQueue.main.async { self?.layout() }
            },
            onDismiss: { [weak self] in self?.dismissWords() },
            attachments: attach == nil ? .none : attachmentUI,
            offer: offer.kind, onOffer: { [weak self] in self?.takeOffer() })
            .onPreferenceChange(ChipFrameKey.self) { [weak self] frame in
                MainActor.assumeIsolated { self?.chipFrame = frame }
            })
    }

    private func layout() {
        // The toggle is always there, so the row shows whenever Pixie does (#221).
        guard pixie.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            hide()
            return
        }
        if !model.open && !cardUp && offer.kind == nil { chipHeight = size.height }
        let top = ScreenDisplays.primaryMaxY
        let bit = pixie.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? pixie.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        // Split up: as one expression it takes CI's type checker too long (#192).
        let agentRows: Int = min(model.view.agents.count, AgentRosterModel.maxRows + 1)
        let yourRows: Int = min(model.view.yours.count, AgentRosterModel.maxRows + 1)
        let listHeight: CGFloat = CGFloat(agentRows + yourRows + 3) * 22
        let openHeight: CGFloat = max(listHeight, ChipPlacement.textBoxHeight) + chipHeight
        // While open the side stays put: only a closed chip decides it (the real list can be
        // taller than the estimate, and flipping would move it from under the pointer).
        let (rect0, b) = ChipPlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                            panel: size, screen: area, openHeight: openHeight,
                                            keeping: model.open || cardUp ? below : nil)
        var rect = rect0
        if b != below {
            // Redraw with the list on the other side and place once more with the new size. Not
            // via render()/layout(): the side is settled here, so this cannot loop.
            below = b
            setRoot()
            let s = hosting.fittingSize
            rect = ChipPlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                       panel: s, screen: area, openHeight: openHeight, keeping: b).0
            if !(s.width > 0 && s.height > 0) { hide(); return }
        }
        panel.setFrame(ScreenDisplays.appKitFrame(topLeft: rect.origin, size: rect.size), display: true)
        if (model.open || cardUp) && panel.parent != nil {
            // Above the updates bubble (a sibling child panel): re-adding puts it last.
            pixie.panel.removeChildWindow(panel)
        }
        if panel.parent == nil { pixie.panel.addChildWindow(panel, ordered: .above) }
        if !panel.isVisible { panel.orderFrontRegardless() }
        if panel.alphaValue != 1 { panel.alphaValue = 1 }
    }

    /// The card is drawn: the text box, or a caption on its own in voice mode (#224).
    private var cardUp: Bool { chat.open || captionCard }

    private func hide() {
        if panel.parent != nil { pixie.panel.removeChildWindow(panel) }
        if panel.isVisible { panel.orderOut(nil) }
        model.open = false
        setRoot()
    }

    private struct RosterReply: Decodable { var roster: RosterView }
    private struct ModeReply: Decodable {
        var mode: String
        var alwaysShowCaption: Bool?
        var captionDismissSecs: Int?
    }
    private struct SendArgs: Encodable, Sendable { let text: String }
    private struct SendReply: Decodable { var job: String }
    private struct FocusAgentArgs: Encodable, Sendable { let name: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class RosterHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
