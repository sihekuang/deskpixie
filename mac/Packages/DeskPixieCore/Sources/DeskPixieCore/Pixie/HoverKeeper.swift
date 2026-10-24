import CoreGraphics
import Foundation

/// Whether what shows beside Pixie stays up under a wandering pointer (#286): one forgiving
/// region around Pixie, its buttons, the cards and the badge (and the gaps between them), a
/// short grace after the pointer leaves it, and never mid-swipe. The window reports the pointer,
/// the frames and scrolls; times are seconds on one clock, frames in one space.
public struct HoverKeeper: Equatable, Sendable {
    /// How far past the frames the region reaches, on every side.
    public static let padding: CGFloat = 16
    /// How long things stay up after the pointer leaves the region, or a swipe ends.
    public static let grace: TimeInterval = 0.6

    /// The pointer is in the region now.
    public private(set) var inside = false
    private var leftAt: TimeInterval?
    private var scrolling = false
    private var scrollEndedAt: TimeInterval?

    public init() {}

    /// The badge deck at its tallest: a session's menu with its options and typed answer.
    public static let deckMaxHeight: CGFloat = 360

    /// Everywhere a reply card, the text box, the badge or the badge deck can show around Pixie
    /// at this spot (desktop space): the card at its tallest under or over Pixie and the deck at
    /// its widest and tallest beside it, on the side the placement logic picks on this screen.
    /// `chipHeight` is the row of buttons under Pixie; `cardWidth` the card's.
    public static func reach(pixie: CGRect, screen: CGRect, chipHeight: CGFloat = 40,
                             cardWidth: CGFloat = 260) -> [CGRect] {
        let box = CGSize(width: cardWidth, height: ChipPlacement.textBoxHeight + chipHeight)
        let card = ChipPlacement.frame(pixie: pixie, panel: box, screen: screen, openHeight: box.height).0
        let deck = CGSize(width: UpdatesBubbleLayout.maxWidth, height: deckMaxHeight)
        let bubble = BubblePlacement.frame(pixie: pixie, bubble: deck, screen: screen, sideWidth: deck.width).0
        return [pixie, card, bubble]
    }

    /// The region: everything that shows, the gaps between included, padded.
    public static func region(_ frames: [CGRect]) -> CGRect? {
        let shown = frames.filter { !$0.isNull && !$0.isEmpty }
        guard let first = shown.first else { return nil }
        return shown.dropFirst().reduce(first) { $0.union($1) }.insetBy(dx: -padding, dy: -padding)
    }

    /// Where the pointer is, and the frames of what shows now.
    public mutating func pointer(at p: CGPoint, frames: [CGRect], now: TimeInterval) {
        let isIn = Self.region(frames)?.contains(p) ?? false
        if inside && !isIn { leftAt = now }
        if isIn { leftAt = nil }
        inside = isIn
    }

    /// A swipe on a deck started (true) or came to rest (false).
    public mutating func scroll(active: Bool, now: TimeInterval) {
        if scrolling && !active { scrollEndedAt = now }
        scrolling = active
    }

    /// Nothing hides or fades: the pointer is in the region or just left it, or a swipe is
    /// under way or just ended.
    public func keepsShowing(now: TimeInterval) -> Bool {
        if inside || scrolling { return true }
        if let leftAt, now - leftAt < Self.grace { return true }
        if let scrollEndedAt, now - scrollEndedAt < Self.grace { return true }
        return false
    }
}
