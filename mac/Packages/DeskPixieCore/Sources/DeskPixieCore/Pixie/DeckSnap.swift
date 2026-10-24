import CoreGraphics

/// Where a deck's swipe comes to rest (#286): always on one card, never between two, and never
/// more than one card from where it started.
public enum DeckSnap {
    /// Below this (points) a drag is a nudge: the deck stays on its card.
    public static let threshold: CGFloat = 24

    /// One swipe, one card: the card the swipe started on (`start`, from 0), or its neighbour
    /// in the direction it `moved` (+ towards later cards) once it moved `threshold` or more,
    /// however far a flick would carry it. Kept within the `count` cards.
    public static func step(from start: Int, moved: CGFloat, count: Int) -> Int {
        guard count > 0 else { return 0 }
        let from = min(max(0, start), count - 1)
        guard abs(moved) >= threshold else { return from }
        return min(max(0, from + (moved > 0 ? 1 : -1)), count - 1)
    }

    /// The card nearest the middle, from 0: `offset` is how far the cards have scrolled (0 with
    /// the first card in place), `stride` a card's width plus the gap. Past halfway to the next
    /// card it is the next one.
    public static func nearest(offset: CGFloat, stride: CGFloat) -> Int {
        guard stride > 0 else { return 0 }
        return max(0, Int((offset / stride).rounded()))
    }
}
