import AppKit
import DeskPixieCore
import os
import SwiftUI

/// The updates bubble beside Bit (spec docs/specs/2026-10-14-updates-bubble-design.md): a child
/// panel of the mascot panel showing `UpdatesBubbleModel`'s phase. It pops when an update
/// arrives, shrinks to a count badge, opens under the pointer, and tells the core what was seen
/// (`agent.updatesSeen`) and which line was clicked (`agent.focusJob`).
///
/// Usage (coordinator, main actor):
/// ```swift
/// let bubble = UpdatesBubbleController(engine: engine, worker: worker, mascot: mascot)
/// bubble.onTalk = { coordinator.talk() }
/// // .updates → apply(lines:), settings.changed → apply(settings:), presence → presence(present:)
/// ```
/// The pointer is polled (like Bit's cursor poll) rather than tracked: the panel resizes under
/// the pointer when it opens, and a faded (alpha 0) window gets no tracking events.
@MainActor
final class UpdatesBubbleController {
    static let pollInterval: TimeInterval = 0.1

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "bubble")

    private let engine: CoreEngine
    private let worker: CoreWorker
    private let mascot: MascotController
    private let panel: MascotPanel
    private let hosting: BubbleHostingView

    private(set) var model = UpdatesBubbleModel()
    private(set) var settings = BubbleSettingsValues.defaults
    /// False while away (screen locked, displays asleep or idle): pops are held.
    private(set) var present = true
    /// "+N more": start a conversation (the voice hotkey; Bit replays everything).
    var onTalk: (() -> Void)?

    private var timer: Timer?
    private var pointerInside = false
    /// Bit's proximity-fade opacity, which the bubble follows unless the pointer is on it.
    private var mascotOpacity: CGFloat = 1
    /// Counts `updates` events, so a slower `agent.updates` read never overwrites a newer list.
    private var eventsApplied = 0

    init(engine: CoreEngine, worker: CoreWorker, mascot: MascotController) {
        self.engine = engine
        self.worker = worker
        self.mascot = mascot
        panel = MascotPanel(frame: NSRect(x: 0, y: 0, width: 1, height: 1))
        hosting = BubbleHostingView(rootView: AnyView(EmptyView()))
        panel.contentView = hosting
        mascot.onFrameChange = { [weak self] _ in self?.layout() }
        mascot.onOpacityChange = { [weak self] opacity in
            self?.mascotOpacity = opacity
            self?.updateAlpha()
        }
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
        let inside = panel.isVisible && panel.frame.contains(NSEvent.mouseLocation)
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
    /// (the menu bar is hidden) or while Bit is hidden.
    private func popPermission() -> PopPermission {
        if !present { return .hold }
        if !NSMenu.menuBarVisible() || !mascot.isShown { return .badgeOnly }
        return .allowed
    }

    private func render() {
        hosting.rootView = AnyView(UpdatesBubbleView(
            count: model.count, phase: model.phase, shown: model.shown, more: model.more,
            onLine: { [weak self] in self?.clicked($0) },
            onMore: { [weak self] in self?.onTalk?() },
            onBadge: { [weak self] in
                guard let self else { return }
                self.model.pointerEntered(now: Self.now)
                self.render()
            }))
        layout()
    }

    /// Sizes the panel to its content and places it beside Bit (BubblePlacement, desktop space),
    /// or hides it.
    private func layout() {
        guard model.phase != .hidden, settings.enabled, mascot.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            hide()
            return
        }
        let top = ScreenDisplays.primaryMaxY
        let bit = mascot.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? mascot.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        let (rect, _) = BubblePlacement.frame(mascot: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                              bubble: size, screen: area)
        panel.setFrame(ScreenDisplays.appKitFrame(topLeft: rect.origin, size: rect.size), display: true)
        if panel.parent == nil {
            mascot.panel.addChildWindow(panel, ordered: .above)
        }
        if !panel.isVisible { panel.orderFrontRegardless() }
        updateAlpha()
    }

    private func hide() {
        if panel.parent != nil { mascot.panel.removeChildWindow(panel) }
        if panel.isVisible { panel.orderOut(nil) }
        // Hidden under the pointer (bubble turned off, Bit hidden): it was not looked at.
        if pointerInside {
            pointerInside = false
            model.pointerLeft()
        }
    }

    /// Bit's fade, except at full opacity while the pointer is on the bubble.
    private func updateAlpha() {
        let alpha = pointerInside ? 1 : mascotOpacity
        if panel.alphaValue != alpha { panel.alphaValue = alpha }
    }

    private static var now: TimeInterval { ProcessInfo.processInfo.systemUptime }

    private struct UpdatesReply: Decodable { var lines: [UpdateLine] }
    private struct SeenArgs: Encodable, Sendable { let ids: [UInt64] }
    private struct FocusJobArgs: Encodable, Sendable { let job: String; let worker: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class BubbleHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
