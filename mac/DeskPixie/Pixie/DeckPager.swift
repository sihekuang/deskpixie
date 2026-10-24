import SwiftUI

/// A deck's cards side by side, one page at a time (#286): SwiftUI's own horizontal paging, so a
/// two-finger swipe moves one card and the neighbours peek in at the edges. The model stays the
/// source of truth: `selected` comes from it, a scroll by you goes back through `onSelect`, and
/// a change in the model (a new card, an answered one, the arrows) scrolls the pager to it.
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
    let page: (ID) -> Page

    /// How far the neighbours peek in, and the gap between pages.
    static var peek: CGFloat { 14 }
    static var gap: CGFloat { 4 }

    /// Where the scroll view is. It starts on the model's card: left nil, the scroll view would
    /// report its first card (the oldest) and move the model there at launch.
    @State private var scrolled: ID?
    @State private var heights: [ID: CGFloat] = [:]
    /// A scroll by you is under way (macOS 15 tells): only then does a new position go back
    /// to the model, never when the view moves itself (a resize, a programmatic scroll).
    @State private var userScroll = false

    init(ids: [ID], selected: ID?, width: CGFloat, onSelect: @escaping (ID) -> Void,
         onResize: @escaping () -> Void = {}, onScroll: @escaping (Bool) -> Void = { _ in },
         @ViewBuilder page: @escaping (ID) -> Page) {
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

    var body: some View {
        ScrollView(.horizontal) {
            HStack(alignment: .top, spacing: Self.gap) {
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
        .modifier(UserScrollWatch(userScroll: $userScroll, onChange: onScroll) { forward(scrolled) })
        .frame(width: width, height: selected.flatMap { heights[$0] }, alignment: .topLeading)
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
            if userScroll || !UserScrollWatch.knowsPhases { forward(new) }
        }
    }

    private func forward(_ id: ID?) {
        if let id, id != selected { onSelect(id) }
    }
}

/// Tells when a scroll is yours (macOS 15's scroll phases), and when it has come to rest.
private struct UserScrollWatch: ViewModifier {
    @Binding var userScroll: Bool
    let onChange: (Bool) -> Void
    let onRest: () -> Void

    static var knowsPhases: Bool {
        if #available(macOS 15, *) { return true }
        return false
    }

    func body(content: Content) -> some View {
        if #available(macOS 15, *) {
            content.onScrollPhaseChange { _, phase in
                let yours = phase == .tracking || phase == .interacting || phase == .decelerating
                if userScroll && !yours { onRest() }
                if userScroll != yours { onChange(yours) }
                userScroll = yours
            }
        } else {
            content
        }
    }
}

/// Each page's height, so the pager is as tall as the page shown.
private struct PageHeights: PreferenceKey {
    static var defaultValue: [AnyHashable: CGFloat] { [:] }
    static func reduce(value: inout [AnyHashable: CGFloat], nextValue: () -> [AnyHashable: CGFloat]) {
        value.merge(nextValue()) { $1 }
    }
}

private extension View {
    /// One card per swipe: always by one on macOS 15; on 14 a hard flick may go further.
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
