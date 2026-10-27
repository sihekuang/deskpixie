import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The updates bubble beside Pixie (spec docs/specs/2026-10-14-updates-bubble-design.md): a child
/// panel of the pixie panel showing `UpdatesBubbleModel`'s phase. It pops when an update
/// arrives, shrinks to a count badge and opens under the pointer as the badge deck (#286); a
/// click on a card brings up its pane (`agent.focusSession`, `agent.focusJob`).
///
/// Usage (coordinator, main actor):
/// ```swift
/// let bubble = UpdatesBubbleController(engine: engine, worker: worker, pixie: pixie)
/// // .updates → apply(lines:), settings.changed → apply(settings:), presence → presence(present:)
/// ```
/// The pointer is polled (like Pixie's cursor poll) rather than tracked: the panel resizes under
/// the pointer when it opens, and a faded (alpha 0) window gets no tracking events.
@MainActor
final class UpdatesBubbleController {
    static let pollInterval: TimeInterval = 0.1

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "bubble")

    private let engine: CoreEngine
    /// The links and files in the lines' text (#277).
    private let richText: RichTextStore
    private let worker: CoreWorker
    private let pixie: PixieController
    private let panel: PixiePanel
    private let hosting: BubbleHostingView

    private(set) var model = UpdatesBubbleModel()
    /// An agent's pane waiting to be brought up once the user stops typing (#110).
    private var paneGate = PaneShowGate()
    private(set) var settings = BubbleSettingsValues.defaults
    /// False while away (screen locked, displays asleep or idle): pops are held.
    private(set) var present = true

    private var timer: Timer?
    private var pointerInside = false
    /// Once open, the bubble stays open while the pointer is around Pixie and its cards, or a
    /// swipe runs (#286).
    private var keeper = HoverKeeper()
    /// The typed answer's draft (#268), and the monitor that puts the field away on a click in
    /// another app.
    private let typed = BubbleTyping()
    private let picks = BubblePicks()
    private var clickAway: Any?
    /// Counts `updates` events, so a slower `agent.updates` read never overwrites a newer list.
    private var eventsApplied = 0

    /// Opens the chat window at an entry (#388); set by whoever owns the chat window.
    var onOpenChat: ((String) -> Void)?

    init(engine: CoreEngine, worker: CoreWorker, pixie: PixieController) {
        self.engine = engine
        self.worker = worker
        self.pixie = pixie
        richText = RichTextStore(engine: engine)
        panel = PixiePanel(frame: NSRect(x: 0, y: 0, width: 1, height: 1))
        hosting = BubbleHostingView(rootView: AnyView(EmptyView()))
        panel.contentView = hosting
        pixie.observeFrame { [weak self] _ in self?.layout() }
        richText.onChange = { [weak self] in DispatchQueue.main.async { self?.layout() } }
        timer = Timer.scheduledTimer(withTimeInterval: Self.pollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.poll() }
        }
        RunLoop.main.add(timer!, forMode: .common)
        render()
        fetchCurrent()
    }

    /// Stops the poll and hides the panel.
    func stop() {
        timer?.invalidate()
        timer = nil
        hide()
    }

    // MARK: Inputs

    /// The core's current list (`updates` event).
    func apply(lines: [UpdateLine]) {
        eventsApplied += 1
        model.apply(lines, now: Self.now, pop: popPermission())
        if model.typing == nil { releaseKey() } // its question went while you typed
        let waiting = MenuPicks.waiting(picks.picked, in: lines) // #364
        if waiting.count != picks.picked.count { picks.picked = waiting }
        render()
    }

    // MARK: Typed answers (#268)

    private func startTyping(_ token: String) {
        guard model.acceptsDecision(token: token, now: Self.now, at: NSEvent.mouseLocation),
              model.startTyping(token: token) else { return }
        typed.draft = ""
        panel.acceptsKey = true
        render()
        panel.makeKey()
        typed.focusRequests += 1
        if clickAway == nil {
            clickAway = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
                MainActor.assumeIsolated { self?.stopTyping() }
            }
        }
    }

    private func sendTyped(_ token: String) {
        let text = typed.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard model.typing == token, !text.isEmpty else { return }
        call("agent.typedAnswer", TypedArgs(token: token, text: text))
        stopTyping()
        acted(token: token)
    }

    /// You acted on a card (#286): it leaves the deck at once, the next slides in.
    private func acted(token: String) {
        guard let line = model.lines.first(where: { $0.token == token }) else { return }
        acted(line)
    }

    private func acted(_ line: UpdateLine) {
        model.act(on: line.id, now: Self.now)
        SystemNotificationSink.removeNotifications(updates: [line.id])
        render()
    }

    private func stopTyping() {
        releaseKey()
        model.stopTyping(pointerInside: pointerInside)
        render()
    }

    /// The field is gone: the panel goes back to never taking keys.
    private func releaseKey() {
        if let m = clickAway {
            NSEvent.removeMonitor(m)
            clickAway = nil
        }
        typed.draft = ""
        guard panel.acceptsKey else { return }
        panel.acceptsKey = false
        if panel.isKeyWindow { panel.resignKey() }
    }

    /// `notifications.bubble.*` (live).
    func apply(settings s: BubbleSettingsValues) {
        settings = s
        model.popSecs = TimeInterval(s.popSecs)
        model.maxLines = s.lines
        render()
    }

    /// Presence changed; on return, one pop if anything arrived while away.
    func presence(present p: Bool) {
        guard p != present else { return }
        present = p
        if p { model.back(now: Self.now, pop: popPermission()) }
        render()
    }

    // MARK: Engine

    /// The list as it is now: the core may have sent its first `updates` event before this
    /// bubble existed.
    private func fetchCurrent() {
        let engine = engine, worker = worker, since = eventsApplied
        Task { @MainActor [weak self] in
            let lines: [UpdateLine]?? = await worker.run {
                do {
                    return try engine.call("agent.updates", as: UpdatesReply.self).lines
                } catch {
                    Self.log.error("agent.updates: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            guard let self, let lines = lines ?? nil, self.eventsApplied == since else { return }
            self.apply(lines: lines)
        }
    }

    private func call(_ method: String, _ args: some Encodable & Sendable) {
        let engine = engine
        worker.async {
            do {
                _ = try engine.call(method, args)
            } catch {
                Self.log.error("\(method, privacy: .public): \(String(describing: error), privacy: .public)")
            }
        }
    }

    /// An agent waits at a prompt in its terminal (`showPane`): bring its pane up as a click on
    /// its line would, once the user hasn't typed for a moment (the next poll at the earliest).
    func showPane(_ r: PaneRequest) {
        paneGate.request(r, now: Self.now)
    }

    /// Seconds since the user last pressed a key in any app (no permission needed).
    private static func sinceLastKey() -> TimeInterval {
        CGEventSource.secondsSinceLastEventType(.combinedSessionState, eventType: .keyDown)
    }

    /// A click on a card (#286): its pane comes up, and it stays in the deck until it is
    /// answered or no longer pending (the core drops it then).
    private func clicked(_ line: UpdateLine) {
        SystemNotificationSink.removeNotifications(updates: [line.id])
        focus(line)
    }

    /// Brings up the line's pane: your session's own tab, else its app, else the job's pane.
    private func focus(_ line: UpdateLine) {
        if let session = line.session, !session.isEmpty {
            call("agent.focusSession", FocusSessionArgs(name: session))
        } else if let app = line.app {
            AppActivation.activate(bundleID: app)
        } else {
            call("agent.focusJob", FocusJobArgs(job: line.job ?? "", worker: line.worker ?? ""))
        }
    }

    /// A click on a line's Mac notification (#199): as a click on the line. An old notification
    /// whose id now names another line (ids can repeat across restarts) only brings up its pane.
    func openFromNotification(_ line: UpdateLine) {
        guard model.isStale(line) else { return clicked(line) }
        focus(line)
    }

    // MARK: Poll

    private func poll() {
        // A badge faded with Pixie is not looked at: a passing pointer doesn't open it.
        let mouse = NSEvent.mouseLocation
        keeper.pointer(at: pixie.hoverPointer, frames: pixie.hoverFrames, now: Self.now)
        let onPanel = panel.isVisible && panel.frame.contains(mouse)
        // Closed with Hide: it opens again on hover once the pointer has been away.
        if !keeper.inside { model.regionLeft() }
        // Opened only on the badge or bubble itself; once open, the whole region keeps it.
        let inside = model.collapsed ? false : pointerInside
            ? onPanel || keeper.keepsShowing(now: Self.now)
            : UpdatesBubbleModel.hoverCounts(pointerOnPanel: onPanel, alpha: panel.alphaValue, alreadyInside: false)
        var changed = false
        if inside != pointerInside {
            pointerInside = inside
            if inside { model.pointerEntered(now: Self.now, at: NSEvent.mouseLocation) } else { model.pointerLeft() }
            changed = true
        }
        if let r = paneGate.due(now: Self.now, sinceLastKey: Self.sinceLastKey()) {
            call("agent.focusJob", FocusJobArgs(job: r.job, worker: r.worker))
        }
        let before = model.phase
        // A pop doesn't shrink to the badge while you're around (#286).
        model.tick(now: Self.now, holding: keeper.keepsShowing(now: Self.now))
        if changed || model.phase != before { render() } else { updateAlpha() }
    }

    // MARK: Drawing

    /// Badge only while away (held for one pop on return), when a fullscreen app is in front
    /// or while Pixie is hidden.
    private func popPermission() -> PopPermission {
        if !present { return .hold }
        if !pixie.isShown || Self.fullscreenAppInFront() { return .badgeOnly }
        return .allowed
    }

    /// The frontmost app (not DeskPixie) has an on-screen, layer-0 window covering a whole
    /// screen. Not `NSMenu.menuBarVisible()`, which is false all the time when the menu bar
    /// hides automatically. Bounds and pid need no screen-recording permission.
    private static func fullscreenAppInFront() -> Bool {
        guard let app = NSWorkspace.shared.frontmostApplication,
              app.processIdentifier != ProcessInfo.processInfo.processIdentifier,
              let info = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements],
                                                    kCGNullWindowID) as? [[String: Any]]
        else { return false }
        let pid = Int(app.processIdentifier)
        let windows: [CGRect] = info.compactMap { w in
            guard (w[kCGWindowOwnerPID as String] as? Int) == pid,
                  (w[kCGWindowLayer as String] as? Int) == 0,
                  let b = w[kCGWindowBounds as String] as? NSDictionary
            else { return nil }
            return CGRect(dictionaryRepresentation: b as CFDictionary)
        }
        let top = ScreenDisplays.primaryMaxY
        let screens = NSScreen.screens.map { DesktopSpace.desktopRect($0.frame, primaryMaxY: top) }
        return Fullscreen.isFullscreen(windows: windows, screens: screens)
    }

    private func render() {
        hosting.rootView = AnyView(UpdatesBubbleView(
            count: model.count, phase: model.phase, lines: model.lines, current: model.current,
            width: UpdatesBubbleView.width(for: model.lines),
            onLine: { [weak self] in self?.clicked($0) },
            onSelect: { [weak self] in
                guard let self else { return }
                self.model.show(id: $0, now: Self.now)
                self.render()
            },
            onResize: { [weak self] in DispatchQueue.main.async { self?.layout() } },
            onScroll: { [weak self] in self?.keeper.scroll(active: $0, now: Self.now) },
            onDecide: { [weak self] token, allow in
                // A tap within decisionGuard of the buttons appearing is a stray click: ignored.
                guard let self, self.model.acceptsDecision(token: token, now: Self.now, at: NSEvent.mouseLocation) else { return }
                self.call("agent.decide", DecideArgs(token: token, allow: allow))
                self.acted(token: token)
            },
            onChoose: { [weak self] token, option in
                guard let self, self.model.acceptsDecision(token: token, now: Self.now, at: NSEvent.mouseLocation) else { return }
                self.call("agent.choose", ChooseArgs(token: token, option: option))
                self.acted(token: token)
            },
            onBadge: { [weak self] in
                guard let self, self.panel.alphaValue >= UpdatesBubbleModel.hoverMinAlpha else { return }
                self.model.regionLeft() // a click on the badge opens it, closed with Hide or not
                self.model.pointerEntered(now: Self.now, at: NSEvent.mouseLocation)
                self.render()
            },
            typing: model.typing, typed: typed,
            onStartTyping: { [weak self] in self?.startTyping($0) },
            onSendTyped: { [weak self] in self?.sendTyped($0) },
            onStopTyping: { [weak self] in self?.stopTyping() },
            onChat: { [weak self] token in
                guard let self, self.model.acceptsDecision(token: token, now: Self.now, at: NSEvent.mouseLocation) else { return }
                self.call("agent.chatAbout", ChatArgs(token: token))
                self.acted(token: token)
            },
            picks: picks,
            onPick: { [weak self] token, options in
                guard let self, !options.isEmpty,
                      self.model.acceptsDecision(token: token, now: Self.now, at: NSEvent.mouseLocation) else { return }
                self.call("agent.pick", PickArgs(token: token, options: options))
                self.picks.picked[token] = nil
                self.acted(token: token)
            },
            onTerminal: { [weak self] line in
                self?.focus(line) // the card stays until it is answered there (#347)
            },
            onDismiss: { [weak self] line in
                guard let self else { return }
                self.call("agent.dismiss", DismissArgs(id: line.id)) // the core's updates take it off
            },
            onOpenChat: { [weak self] line in self?.onOpenChat?(line.chatEntryID) },
            onCollapse: { [weak self] in
                guard let self else { return }
                self.releaseKey()
                self.model.collapse()
                self.pointerInside = false
                self.render()
            }).environmentObject(richText))
        layout()
    }

    /// Sizes the panel to its content and places it beside Pixie (BubblePlacement, desktop space),
    /// or hides it.
    /// A setting changed: where files open may have too, so the lines fetch their spans again.
    func settingsChanged() { richText.reset() }

    private func layout() {
        guard model.phase != .hidden, settings.enabled, pixie.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            // Right after a new root view (a swipe's card, say) SwiftUI may not have measured it
            // yet: keep the panel where it is and try again, rather than hide it for a moment.
            if panel.isVisible && zeroSizeRetries < 3 {
                zeroSizeRetries += 1
                DispatchQueue.main.async { [weak self] in self?.layout() }
            } else {
                hide()
            }
            return
        }
        zeroSizeRetries = 0
        let top = ScreenDisplays.primaryMaxY
        let bit = pixie.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? pixie.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        // The side comes from the open bubble's width in every phase: the badge and the bubble
        // that opens from it stay on the same side, next to Pixie (no flip under the pointer).
        let (rect, _) = BubblePlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                              bubble: size, screen: area,
                                              sideWidth: UpdatesBubbleLayout.maxWidth)
        panel.setFrame(ScreenDisplays.appKitFrame(topLeft: rect.origin, size: rect.size), display: true)
        if panel.parent == nil {
            pixie.panel.addChildWindow(panel, ordered: .above)
        }
        if !panel.isVisible { panel.orderFrontRegardless() }
        updateAlpha()
    }

    /// Layouts in a row that found the view unmeasured (see `layout`).
    private var zeroSizeRetries = 0

    private func hide() {
        if panel.parent != nil { pixie.panel.removeChildWindow(panel) }
        if panel.isVisible { panel.orderOut(nil) }
        // Hidden under the pointer (bubble turned off, Pixie hidden): it was not looked at.
        if pointerInside {
            pointerInside = false
            model.pointerLeft()
        }
    }

    /// Always opaque: the bubble doesn't follow Pixie's proximity fade, or the badge would vanish
    /// exactly when the pointer comes to open it. It still hides whenever Pixie is hidden.
    private func updateAlpha() {
        let alpha: CGFloat = 1
        if panel.alphaValue != alpha { panel.alphaValue = alpha }
    }

    private static var now: TimeInterval { ProcessInfo.processInfo.systemUptime }

    private struct UpdatesReply: Decodable { var lines: [UpdateLine] }
    private struct DecideArgs: Encodable, Sendable { let token: String; let allow: Bool }
    private struct ChooseArgs: Encodable, Sendable { let token: String; let option: Int }
    private struct PickArgs: Encodable, Sendable { let token: String; let options: [Int] }
    private struct TypedArgs: Encodable, Sendable { let token: String; let text: String }
    private struct ChatArgs: Encodable, Sendable { let token: String }
    private struct DismissArgs: Encodable, Sendable { let id: UInt64 }
    private struct FocusJobArgs: Encodable, Sendable { let job: String; let worker: String }
    private struct FocusSessionArgs: Encodable, Sendable { let name: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class BubbleHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
