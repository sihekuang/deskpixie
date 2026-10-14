import CoreGraphics

/// Where the updates bubble sits beside Bit. Desktop coordinates (`DesktopSpace`: top-left
/// origin, y down), like `PixieSettings.position`.
public enum BubblePlacement {
    public enum Side: Sendable { case left, right }

    /// Desktop-space rects (top-left origin). Prefers the right of Bit; flips left when a
    /// `sideWidth`-wide bubble wouldn't fit; clamps vertically into `screen`. Bubble top aligns
    /// with Bit's top. When neither side fits, the bubble is also clamped horizontally so it
    /// stays on screen.
    ///
    /// `sideWidth` (default: the bubble's own width) picks the side. Pass the open bubble's
    /// width for every phase, so the badge sits on the same side as the bubble that opens from
    /// it, next to Bit, and the open bubble covers the badge (no flip under the pointer).
    public static func frame(pixie: CGRect, bubble: CGSize, screen: CGRect, gap: CGFloat = 8,
                             sideWidth: CGFloat? = nil) -> (CGRect, Side) {
        let right = pixie.maxX + gap
        let side: Side = right + max(sideWidth ?? bubble.width, bubble.width) <= screen.maxX ? .right : .left
        var x = side == .right ? right : pixie.minX - gap - bubble.width
        x = clamp(x, screen.minX, screen.maxX - bubble.width)
        let y = clamp(pixie.minY, screen.minY, screen.maxY - bubble.height)
        return (CGRect(x: x, y: y, width: bubble.width, height: bubble.height), side)
    }

    /// `lo` wins when the range is empty (a bubble bigger than the screen keeps its top/left on it).
    private static func clamp(_ v: CGFloat, _ lo: CGFloat, _ hi: CGFloat) -> CGFloat {
        max(lo, min(v, hi))
    }
}
