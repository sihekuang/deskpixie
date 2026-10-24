import AppKit
import DeskPixieCore
import SwiftUI

/// A horizontal two-finger swipe on a deck (#286): one card per gesture (`DeckSwipe`), −1 older,
/// +1 newer. Put behind the deck with `.background`. It watches the scroll events of its own
/// window under its bounds and never takes them, so clicks reach the cards' links and buttons and
/// long text still scrolls. A mouse wheel (no precise deltas) does nothing here.
struct DeckSwipeCatcher: NSViewRepresentable {
    let onStep: (Int) -> Void

    func makeNSView(context: Context) -> CatcherView {
        let v = CatcherView()
        v.onStep = onStep
        return v
    }

    func updateNSView(_ v: CatcherView, context: Context) { v.onStep = onStep }

    final class CatcherView: NSView {
        var onStep: (Int) -> Void = { _ in }
        private var swipe = DeckSwipe()
        /// The gesture began over this view: its later events count wherever the pointer is.
        private var tracking = false
        // Read in deinit too (nonisolated); only ever touched on the main thread.
        nonisolated(unsafe) private var monitor: Any?
        /// When this gesture began (its `.began` event's timestamp).
        private var gesture: TimeInterval = 0
        /// The gesture that last stepped a deck, app-wide: one physical swipe never steps twice,
        /// even if a redraw leaves a second catcher listening for a moment.
        private static var stepped: TimeInterval = -1

        deinit {
            if let monitor { NSEvent.removeMonitor(monitor) }
        }

        override func hitTest(_ point: NSPoint) -> NSView? { nil }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            if let monitor { NSEvent.removeMonitor(monitor) }
            monitor = nil
            guard window != nil else { return }
            monitor = NSEvent.addLocalMonitorForEvents(matching: .scrollWheel) { [weak self] event in
                MainActor.assumeIsolated { self?.feed(event) }
                return event
            }
        }

        private func feed(_ event: NSEvent) {
            guard let window, event.window === window, event.hasPreciseScrollingDeltas else { return }
            let phase: DeckSwipe.Phase
            if !event.momentumPhase.isEmpty {
                phase = .momentum
            } else if event.phase.contains(.began) {
                tracking = bounds.contains(convert(event.locationInWindow, from: nil))
                gesture = event.timestamp
                phase = .began
            } else if event.phase.contains(.changed) {
                phase = .changed
            } else if event.phase.contains(.ended) || event.phase.contains(.cancelled) {
                phase = .ended
            } else {
                return
            }
            guard tracking else { return }
            if phase == .ended { tracking = false }
            let step = swipe.feed(deltaX: event.scrollingDeltaX, deltaY: event.scrollingDeltaY, phase: phase)
            guard step != 0, Self.stepped != gesture else { return }
            Self.stepped = gesture
            onStep(step)
        }
    }
}
