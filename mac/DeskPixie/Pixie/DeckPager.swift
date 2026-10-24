import SwiftUI

/// A deck's cards side by side, one page at a time (#286): SwiftUI's own horizontal paging, so a
/// two-finger swipe moves one card and the neighbours peek in at the edges. The model stays the
/// source of truth: `selected` comes from it, a scroll by you goes back through `onSelect`, and
/// a change in the model (a new card, an answered one, the arrows) scrolls the pager to it.
struct DeckPager<ID: Hashable, Page: View>: View {
    let ids: [ID]
    let selected: ID?
    /// The card's inner width: pages are a little narrower, so their neighbours show.
    let width: CGFloat
    /// Where the pager starts before it has a page: the newest end (reply deck) or the oldest.
    var anchor: UnitPoint = .trailing
    let onSelect: (ID) -> Void
    /// The pager changed height (another page came up): the panel is sized from the view.
    var onResize: () -> Void = {}
    @ViewBuilder let page: (ID) -> Page

    /// How far the neighbours peek in, and the gap between pages.
    static var peek: CGFloat { 12 }
    static var gap: CGFloat { 4 }

    @State private var scrolled: ID?
    @State private var heights: [ID: CGFloat] = [:]
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
        .contentMargins(.horizontal, Self.peek - Self.gap / 2, for: .scrollContent)
        .deckPaging()
        .scrollPosition(id: $scrolled)
        .defaultScrollAnchor(anchor)
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
            if let new, new != selected { onSelect(new) }
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
