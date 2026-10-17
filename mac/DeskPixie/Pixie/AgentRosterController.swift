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
        fetchCurrent()
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        hide()
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
        // Opens under the pointer; closes when it leaves the chip and the list.
        let inside = panel.isVisible && panel.frame.contains(NSEvent.mouseLocation) && model.visible
        if inside != model.open {
            model.open = inside
            render()
        }
    }

    private func render() {
        setRoot()
        layout()
    }

    private func setRoot() {
        hosting.rootView = AnyView(AgentRosterView(
            model: model, below: below, maxChipWidth: max(40, pixie.panel.frame.width),
            onChip: { [weak self] in
                // Hover already opens the list; a click only opens it (never closes, so click and
                // hover poll can't fight). Leaving the panel closes it.
                guard let self, !self.model.open else { return }
                self.model.open = true
                self.render()
            },
            onRow: { [weak self] in self?.clicked($0) }))
    }

    private func layout() {
        guard model.visible, pixie.isShown else {
            hide()
            return
        }
        let size = hosting.fittingSize
        guard size.width > 0, size.height > 0 else {
            hide()
            return
        }
        if !model.open { chipHeight = size.height }
        let top = ScreenDisplays.primaryMaxY
        let bit = pixie.panel.frame
        let screen = NSScreen.screens.first { $0.frame.contains(NSPoint(x: bit.midX, y: bit.midY)) }
            ?? pixie.panel.screen ?? NSScreen.main
        let area = DesktopSpace.desktopRect(screen?.visibleFrame ?? bit, primaryMaxY: top)
        let openHeight = CGFloat(min(model.view.agents.count, AgentRosterModel.maxRows + 1)
                                 + min(model.view.yours.count, AgentRosterModel.maxRows + 1) + 3) * 22 + chipHeight
        // While open the side stays put: only a closed chip decides it (the real list can be
        // taller than the estimate, and flipping would move it from under the pointer).
        let (rect0, b) = ChipPlacement.frame(pixie: DesktopSpace.desktopRect(bit, primaryMaxY: top),
                                            panel: size, screen: area, openHeight: openHeight,
                                            keeping: model.open ? below : nil)
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
        if model.open && panel.parent != nil {
            // Above the updates bubble (a sibling child panel): re-adding puts it last.
            pixie.panel.removeChildWindow(panel)
        }
        if panel.parent == nil { pixie.panel.addChildWindow(panel, ordered: .above) }
        if !panel.isVisible { panel.orderFrontRegardless() }
        if panel.alphaValue != 1 { panel.alphaValue = 1 }
    }

    private func hide() {
        if panel.parent != nil { pixie.panel.removeChildWindow(panel) }
        if panel.isVisible { panel.orderOut(nil) }
        model.open = false
        setRoot()
    }

    private struct RosterReply: Decodable { var roster: RosterView }
    private struct FocusAgentArgs: Encodable, Sendable { let name: String }
}

/// Takes the first click even though the panel never becomes key (non-activating).
private final class RosterHostingView: NSHostingView<AnyView> {
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }
}
