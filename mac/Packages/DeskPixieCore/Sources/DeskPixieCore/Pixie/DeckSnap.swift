import CoreGraphics

/// Where a deck's swipe comes to rest (#286): always on one card, never between two.
public enum DeckSnap {
    /// The card nearest the middle, from 0: `offset` is how far the cards have scrolled (0 with
    /// the first card in place), `stride` a card's width plus the gap. Past halfway to the next
    /// card it is the next one.
    public static func nearest(offset: CGFloat, stride: CGFloat) -> Int {
        guard stride > 0 else { return 0 }
        return max(0, Int((offset / stride).rounded()))
    }
}
