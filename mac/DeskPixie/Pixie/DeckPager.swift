import DeskPixieCore
import SwiftUI

/// A deck's cards side by side, one page at a time (#286): SwiftUI's own horizontal paging, the
/// neighbours peeking in at the edges, and one card per swipe however hard the flick
/// (`DeckSnap.step`). The model stays the source of truth: `selected` comes from it, a swipe by
/// you goes back through `onSelect` once it rests, and a change in the model (a new card, an
/// answered one, the arrows) scrolls the pager to it. The deck is as tall as its tallest card,
/// so nothing is clipped and the panel doesn't resize while you swipe; each card keeps its own
/// height, against the edge nearest Pixie.
struct DeckPager<ID: Hashable, Page: View>: View {
    let ids: [ID]
    let selected: ID?
    /// The deck's width: cards are a little narrower, so their neighbours peek in at the sides.
    let width: CGFloat
    let onSelect: (ID) -> Void
    /// The pager changed height (another page came up): the panel is sized from the view.
    var onResize: () -> Void = {}
    /// A swipe on the deck started (true) or came to rest (false): nothing hides meanwhile.
    var onScroll: (Bool) -> Void = { _ in }
    /// The deck shows above Pixie: shorter cards sit at the bottom, next to it.
    var bottomAligned = false
    let page: (ID) -> Page

    /// How far the neighbours peek in, and the gap between pages.
    static var peek: CGFloat { 14 }
    static var gap: CGFloat { 4 }

    /// Where the scroll view is. It starts on the model's card: left nil, the scroll view would
    /// report its first card (the oldest) and move the model there at launch.
    @State private var scrolled: ID?
    @State private var heights: [ID: CGFloat] = [:]

    init(ids: [ID], selected: ID?, width: CGFloat, onSelect: @escaping (ID) -> Void,
         onResize: @escaping () -> Void = {}, onScroll: @escaping (Bool) -> Void = { _ in },
         bottomAligned: Bool = false, @ViewBuilder page: @escaping (ID) -> Page) {
        self.bottomAligned = bottomAligned
        self.ids = ids
        self.selected = selected
        self.width = width
        self.onSelect = onSelect
        self.onResize = onResize
        self.onScroll = onScroll
        self.page = page
        _scrolled = State(initialValue: selected)
    }
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    private var pageWidth: CGFloat { max(40, width - 2 * Self.peek) }
    private var tallest: CGFloat? { heights.values.max() }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView(.horizontal) {
                HStack(alignment: bottomAligned ? .bottom : .top, spacing: Self.gap) {
                    ForEach(ids, id: \.self) { id in
                        page(id)
                            .frame(width: pageWidth, alignment: .topLeading)
                            .background(GeometryReader { g in
                                Color.clear.preference(key: PageHeights.self, value: [AnyHashable(id): g.size.height])
                            })
                            // Off-centre pages sit back a little, so the one in the middle reads as on top.
                            .scrollTransition(.interactive, axis: .horizontal) { content, phase in
                                content
                                    .scaleEffect(reduceMotion || phase.isIdentity ? 1 : 0.95)
                                    .opacity(reduceMotion || phase.isIdentity ? 1 : 0.6)
                            }
                            .id(id)
                    }
                }
                .scrollTargetLayout()
                // An answered card leaves and the next slides into its place.
                .animation(reduceMotion ? nil : .easeOut(duration: 0.25), value: ids)
            }
            .scrollIndicators(.hidden)
            // A card at rest starts at the margin, which is where centring puts it: snapping and
            // the arrows land on the same spot, one card in the middle and both neighbours peeking.
            .contentMargins(.horizontal, Self.peek, for: .scrollContent)
            .deckPaging()
            .scrollPosition(id: $scrolled, anchor: .center)
            .modifier(SwipeWatch(onChange: onScroll, onLift: { offset in
                // One swipe, one card: the neighbour of the card it started on (the model's,
                // which a swipe doesn't change until it rests), centred, however far the fingers
                // or the momentum would take it. Card k rests at offset k × stride.
                guard !ids.isEmpty else { return }
                let stride = pageWidth + Self.gap
                let start = selected.flatMap { ids.firstIndex(of: $0) } ?? DeckSnap.nearest(offset: offset, stride: stride)
                let moved = offset - CGFloat(start) * stride
                let id = ids[DeckSnap.step(from: start, moved: moved, count: ids.count)]
                settle(on: id, proxy)
                forward(id)
            }, onDrift: {
                // Momentum after the swipe was settled: back onto the card it settled on.
                if let id = selected { settle(on: id, proxy) }
            }))
            .frame(width: width, height: tallest, alignment: bottomAligned ? .bottomLeading : .topLeading)
        }
        .onPreferenceChange(PageHeights.self) { new in
            MainActor.assumeIsolated {
                let typed = Dictionary(uniqueKeysWithValues: new.compactMap { k, v in (k.base as? ID).map { ($0, v) } })
                guard typed != heights else { return }
                heights = typed
                onResize()
            }
        }
        .onAppear { scrolled = selected }
        .onChange(of: selected) { _, new in
            guard new != scrolled else { return }
            if reduceMotion {
                scrolled = new
            } else {
                withAnimation(.easeOut(duration: 0.25)) { scrolled = new }
            }
            onResize()
        }
        .onChange(of: scrolled) { _, new in
            // macOS 15 tells the model once the swipe has come to rest (`onRest`), so the
            // panel isn't redrawn mid-swipe; macOS 14 has no scroll phases to tell by.
            if !SwipeWatch.knowsPhases { forward(new) }
        }
    }

    private func forward(_ id: ID?) {
        if let id, id != selected { onSelect(id) }
    }

    private func settle(on id: ID, _ proxy: ScrollViewProxy) {
        withAnimation(reduceMotion ? nil : .easeOut(duration: 0.2)) { proxy.scrollTo(id, anchor: .center) }
        scrolled = id
    }
}

/// Watches a swipe (macOS 15's scroll phases): where the deck was when the fingers lifted
/// (`onLift`, the scroll offset), and momentum that moves it after that (`onDrift`).
private struct SwipeWatch: ViewModifier {
    let onChange: (Bool) -> Void
    let onLift: (CGFloat) -> Void
    let onDrift: () -> Void

    @State private var fingers = false
    /// The deck moved on its own after the lift (momentum, or scrolling that came late).
    @State private var drifting = false

    static var knowsPhases: Bool {
        if #available(macOS 15, *) { return true }
        return false
    }

    func body(content: Content) -> some View {
        if #available(macOS 15, *) {
            content.onScrollPhaseChange { _, phase, context in
                let offset = context.geometry.contentOffset.x + context.geometry.contentInsets.leading
                let down = phase == .tracking || phase == .interacting
                if down && !fingers {
                    fingers = true
                    onChange(true)
                } else if !down && fingers {
                    fingers = false
                    onLift(offset)
                    onChange(false)
                } else if phase == .decelerating && !fingers {
                    drifting = true
                    onDrift()
                } else if phase == .idle && drifting {
                    // It came to rest wherever the drift left it: back onto the settled card.
                    drifting = false
                    onDrift()
                }
            }
        } else {
            content
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

private extension View {
    /// Rests on a card. One card per swipe is held by `SwipeWatch` and `DeckSnap.step`, since
    /// `.alwaysByOne` doesn't hold against a real trackpad's flick.
    @ViewBuilder func deckPaging() -> some View {
        if #available(macOS 15, *) {
            scrollTargetBehavior(.viewAligned(limitBehavior: .alwaysByOne))
        } else {
            scrollTargetBehavior(.viewAligned)
        }
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
