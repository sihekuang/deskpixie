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
        private var monitor: Any?

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
            if step != 0 { onStep(step) }
        }
    }
}
