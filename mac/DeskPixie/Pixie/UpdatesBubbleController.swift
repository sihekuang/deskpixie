import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The updates bubble beside Pixie (spec docs/specs/2026-10-14-updates-bubble-design.md): a child
/// panel of the pixie panel showing `UpdatesBubbleModel`'s phase. It pops when an update
/// arrives, shrinks to a count badge, opens under the pointer, and tells the core what was seen
/// (`agent.updatesSeen`) and which line was clicked (`agent.focusJob`).
///
/// Usage (coordinator, main actor):
/// ```swift
/// let bubble = UpdatesBubbleController(engine: engine, worker: worker, pixie: pixie)
/// bubble.onTalk = { coordinator.talk() }
/// // .updates → apply(lines:), settings.changed → apply(settings:), presence → presence(present:)
/// ```
/// The pointer is polled (like Pixie's cursor poll) rather than tracked: the panel resizes under
/// the pointer when it opens, and a faded (alpha 0) window gets no tracking events.
@MainActor
final class UpdatesBubbleController {
    static let pollInterval: TimeInterval = 0.1

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "bubble")

    private let engine: CoreEngine
    private let worker: CoreWorker
    private let pixie: PixieController
    private let panel: PixiePanel
    private let hosting: BubbleHostingView

    private(set) var model = UpdatesBubbleModel()
    private(set) var settings = BubbleSettingsValues.defaults
    /// False while away (screen locked, displays asleep or idle): pops are held.
    private(set) var present = true
    /// "+N more": start a conversation (the voice hotkey; Pixie replays everything).
    var onTalk: (() -> Void)?

    private var timer: Timer?
    private var pointerInside = false
    /// Counts `updates` events, so a slower `agent.updates` read never overwrites a newer list.
    private var eventsApplied = 0

    init(engine: CoreEngine, worker: CoreWorker, pixie: PixieController) {
        self.engine = engine
        self.worker = worker
        self.pixie = pixie
        panel = PixiePanel(frame: NSRect(x: 0, y: 0, width: 1, height: 1))
        hosting = BubbleHostingView(rootView: AnyView(EmptyView()))
        panel.contentView = hosting
        pixie.observeFrame { [weak self] _ in self?.layout() }
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
        render()
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

    private func clicked(_ line: UpdateLine) {
        call("agent.focusJob", FocusJobArgs(job: line.job ?? "", worker: line.worker ?? ""))
    }

    // MARK: Poll

    private func poll() {
        // A badge faded with Pixie is not looked at: a passing pointer neither opens it nor marks
        // it seen.
        let inside = UpdatesBubbleModel.hoverCounts(
            pointerOnPanel: panel.isVisible && panel.frame.contains(NSEvent.mouseLocation),
            alpha: panel.alphaValue, alreadyInside: pointerInside)
        var changed = false
        if inside != pointerInside {
            pointerInside = inside
            if inside { model.pointerEntered(now: Self.now) } else { model.pointerLeft() }
            changed = true
        }
        let before = model.phase
        let seen = model.tick(now: Self.now)
        if !seen.isEmpty { call("agent.updatesSeen", SeenArgs(ids: seen)) }
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
            count: model.count, phase: model.phase, shown: model.shown, more: model.more,
            onLine: { [weak self] in self?.clicked($0) },
            onMore: { [weak self] in self?.onTalk?() },
            onDecide: { [weak self] token, allow in
                // A tap within decisionGuard of the buttons appearing is a stray click: ignored.
                guard let self, self.model.acceptsDecision(token: token, now: Self.now) else { return }
                self.call("agent.decide", DecideArgs(token: token, allow: allow))
            },
            onBadge: { [weak self] in
                guard let self, self.panel.alphaValue >= UpdatesBubbleModel.hoverMinAlpha else { return }
                self.model.pointerEntered(now: Self.now)
                self.render()
            }))
        layout()
    }

    /// Sizes the panel to its content and places it beside Pixie (BubblePlacement, desktop space),
    /// or hides it.
    private func layout() {
        guard model.phase != .hidden, settings.enabled, pixie.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            hide()
            return
        }
        let top = ScreenDisplays.primaryMaxY
        let bit = pixie.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? pixie.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        // The side comes from the open bubble's width in every phase: the badge and the bubble
        // that opens from it stay on the same side, next to Pixie (no flip under the pointer).
        let (rect, _) = BubblePlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                              bubble: size, screen: area,
                                              sideWidth: UpdatesBubbleView.width)
        panel.setFrame(ScreenDisplays.appKitFrame(topLeft: rect.origin, size: rect.size), display: true)
        if panel.parent == nil {
            pixie.panel.addChildWindow(panel, ordered: .above)
        }
        if !panel.isVisible { panel.orderFrontRegardless() }
        updateAlpha()
    }

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
    private struct SeenArgs: Encodable, Sendable { let ids: [UInt64] }
    private struct DecideArgs: Encodable, Sendable { let token: String; let allow: Bool }
    private struct FocusJobArgs: Encodable, Sendable { let job: String; let worker: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class BubbleHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
