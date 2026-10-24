import AppKit
import DeskPixieCore
import SwiftUI

/// A deck's cards side by side, one in the middle and its neighbours peeking in at the edges
/// (#286). A two-finger swipe moves the cards with the fingers, at most one card either way,
/// and when the fingers lift the deck slides once to the neighbour or back (`DeckDrag`); the
/// swipe's momentum is swallowed, so a flick never carries it further or bounces. The model
/// stays the source of truth: `selected` comes from it, a swipe goes back through `onSelect`, and
/// a change in the model (a new card, an answered one, the arrows) slides the deck to it. The
/// deck is as tall as its tallest card, so nothing is clipped and the panel doesn't resize while
/// you swipe; each card keeps its own height, against the edge nearest Pixie.
struct DeckPager<ID: Hashable, Page: View>: View {
    let ids: [ID]
    let selected: ID?
    /// The deck's width: cards are a little narrower, so their neighbours peek in at the sides.
    let width: CGFloat
    let onSelect: (ID) -> Void
    /// The deck changed height (the cards changed): the panel is sized from the view.
    var onResize: () -> Void = {}
    /// A swipe on the deck started (true) or settled (false): nothing hides meanwhile.
    var onScroll: (Bool) -> Void = { _ in }
    /// The deck shows above Pixie: shorter cards sit at the bottom, next to it.
    var bottomAligned = false
    let page: (ID) -> Page

    /// How far the neighbours peek in, and the gap between cards.
    static var peek: CGFloat { 14 }
    static var gap: CGFloat { 4 }
    /// The one slide: to the next card, back after a nudge, or to the model's card.
    static var slide: Animation { .easeOut(duration: 0.22) }

    /// The card in the middle. It starts on the model's card and moves with it; a swipe moves
    /// it first (in the same animation as the drag letting go) and tells the model.
    @State private var shown: ID?
    /// How far the cards follow the fingers, without animation.
    @State private var drag: CGFloat = 0
    @State private var heights: [ID: CGFloat] = [:]
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    init(ids: [ID], selected: ID?, width: CGFloat, onSelect: @escaping (ID) -> Void,
         onResize: @escaping () -> Void = {}, onScroll: @escaping (Bool) -> Void = { _ in },
         bottomAligned: Bool = false, @ViewBuilder page: @escaping (ID) -> Page) {
        self.ids = ids
        self.selected = selected
        self.width = width
        self.onSelect = onSelect
        self.onResize = onResize
        self.onScroll = onScroll
        self.bottomAligned = bottomAligned
        self.page = page
        _shown = State(initialValue: selected)
    }

    private var pageWidth: CGFloat { max(40, width - 2 * Self.peek) }
    private var stride: CGFloat { pageWidth + Self.gap }
    private var index: Int {
        shown.flatMap { ids.firstIndex(of: $0) } ?? selected.flatMap { ids.firstIndex(of: $0) } ?? max(0, ids.count - 1)
    }
    private var tallest: CGFloat? { heights.values.max() }

    var body: some View {
        let at = CGFloat(index) - drag / stride
        HStack(alignment: bottomAligned ? .bottom : .top, spacing: Self.gap) {
            ForEach(Array(ids.enumerated()), id: \.element) { i, id in
                // Off-centre cards sit back a little, so the one in the middle reads as on top.
                let off = reduceMotion ? 0 : min(1, abs(CGFloat(i) - at))
                page(id)
                    .frame(width: pageWidth, alignment: .topLeading)
                    .background(GeometryReader { g in
                        Color.clear.preference(key: PageHeights.self, value: [AnyHashable(id): g.size.height])
                    })
                    // Shrunk towards the middle, so the edge that peeks in stays in view.
                    .scaleEffect(1 - 0.05 * off, anchor: CGFloat(i) < at ? .trailing : .leading)
                    .opacity(1 - 0.4 * off)
            }
        }
        .fixedSize()
        .offset(x: Self.peek - CGFloat(index) * stride + drag)
        .frame(width: width, height: tallest, alignment: bottomAligned ? .bottomLeading : .topLeading)
        .clipped()
        .contentShape(Rectangle())
        .background(SwipeCatcher { phase, dx, dy in feed(phase, dx: dx, dy: dy) })
        .onPreferenceChange(PageHeights.self) { new in
            MainActor.assumeIsolated {
                let typed = Dictionary(uniqueKeysWithValues: new.compactMap { k, v in (k.base as? ID).map { ($0, v) } })
                guard typed != heights else { return }
                heights = typed
                onResize()
            }
        }
        .onChange(of: selected) { _, new in
            guard new != shown else { return }
            withAnimation(reduceMotion ? nil : Self.slide) { shown = new }
        }
        .onDisappear {
            // Gone mid-swipe: its end is still owed, or nothing would ever hide again.
            drag = 0
            if swipe.cancel() { onScroll(false) }
        }
    }

    @State private var swipe = DeckDrag()

    /// A trackpad event over the deck: true when the deck takes it (it goes no further).
    private func feed(_ phase: DeckDrag.Phase, dx: CGFloat, dy: CGFloat) -> Bool {
        let wasSwiping = swipe.swiping
        let verdict = swipe.feed(phase, dx: dx, dy: dy, stride: stride,
                                 canBack: index > 0, canForward: index < ids.count - 1)
        if swipe.swiping && !wasSwiping { onScroll(true) }
        switch verdict {
        case .pass:
            return false
        case .take:
            drag = swipe.offset
            return true
        case .settle(let step):
            let target = ids.isEmpty ? nil : ids[min(max(0, index + step), ids.count - 1)]
            withAnimation(reduceMotion ? nil : Self.slide) {
                drag = 0
                shown = target
            }
            if let target, target != selected { onSelect(target) }
            onScroll(false)
            return true
        }
    }
}

/// Catches the trackpad's scroll events over the deck (#286) and gives them to `onEvent`, which
/// says whether the deck takes them. Taken ones stop here, momentum included, so nothing under
/// the deck scrolls with a swipe. Clicks never stop here (it is never hit), and a mouse wheel or
/// an up-and-down swipe goes through to what is under it (a long card's text).
private struct SwipeCatcher: NSViewRepresentable {
    let onEvent: (DeckDrag.Phase, CGFloat, CGFloat) -> Bool

    func makeNSView(context: Context) -> CatcherView {
        let v = CatcherView()
        v.onEvent = onEvent
        return v
    }

    func updateNSView(_ v: CatcherView, context: Context) { v.onEvent = onEvent }

    final class CatcherView: NSView {
        var onEvent: (DeckDrag.Phase, CGFloat, CGFloat) -> Bool = { _, _, _ in false }
        // Read in deinit too (nonisolated); only ever touched on the main thread.
        nonisolated(unsafe) private var monitor: Any?
        /// The gesture began over the deck: its later events (momentum too) are the deck's to see.
        private var tracking = false

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
                let taken = MainActor.assumeIsolated { self?.take(event) ?? false }
                return taken ? nil : event
            }
        }

        private func take(_ event: NSEvent) -> Bool {
            guard let window, event.window === window, event.hasPreciseScrollingDeltas else { return false }
            let phase: DeckDrag.Phase
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
                return false
            }
            guard tracking else { return false }
            return onEvent(phase, event.scrollingDeltaX, event.scrollingDeltaY)
        }
    }
}

/// Each card's own height: the deck is as tall as the tallest.
private struct PageHeights: PreferenceKey {
    static var defaultValue: [AnyHashable: CGFloat] { [:] }
    static func reduce(value: inout [AnyHashable: CGFloat], nextValue: () -> [AnyHashable: CGFloat]) {
        value.merge(nextValue()) { $1 }
    }
}

/// ‹ n/m › on a deck's card (#286): this card's place, and its neighbours either side.
struct DeckArrows: View {
    /// From 0.
    let index: Int
    let count: Int
    /// Between the numbers: "/" on reply cards ("2/3"), " of " on badge cards ("2 of 3").
    let label: String
    let onBack: () -> Void
    let onForward: () -> Void

    var body: some View {
        HStack(spacing: 2) {
            arrow("chevron.left", "Previous", enabled: index > 0, onBack)
            Text("\(index + 1)\(label)\(count)")
                .monospacedDigit()
                .accessibilityLabel("\(index + 1) of \(count)")
            arrow("chevron.right", "Next", enabled: index < count - 1, onForward)
        }
    }

    private func arrow(_ symbol: String, _ help: String, enabled: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.caption2.weight(.bold))
                .frame(width: 16, height: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundStyle(enabled ? .primary : .tertiary)
        .disabled(!enabled)
        .help(help)
        .accessibilityLabel(help)
    }
}

extension View {
    /// A deck's card on its own (#286): today's material, radius and edge, nothing behind it.
    func deckCard() -> some View {
        background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }
}

/// Hide on a deck's card (#286), where Notification Center puts "Show less": plain text in the
/// header's colour, highlighted under the pointer.
struct DeckHideButton: View {
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button("Hide", action: action)
            .buttonStyle(.plain)
            .foregroundStyle(hovering ? .primary : .secondary)
            .onHover { hovering = $0 }
            .help("Hide")
            .accessibilityLabel("Hide")
    }
}
