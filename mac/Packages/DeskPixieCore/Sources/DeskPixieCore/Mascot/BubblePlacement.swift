import CoreGraphics

/// Where the updates bubble sits beside Bit. Desktop coordinates (`DesktopSpace`: top-left
/// origin, y down), like `MascotSettings.position`.
public enum BubblePlacement {
    public enum Side: Sendable { case left, right }

    /// Desktop-space rects (top-left origin). Prefers the right of Bit; flips left when it
    /// wouldn't fit; clamps vertically into `screen`. Bubble top aligns with Bit's top.
    /// When neither side fits, the bubble is also clamped horizontally so it stays on screen.
    public static func frame(mascot: CGRect, bubble: CGSize, screen: CGRect, gap: CGFloat = 8) -> (CGRect, Side) {
        let right = mascot.maxX + gap
        let side: Side = right + bubble.width <= screen.maxX ? .right : .left
        var x = side == .right ? right : mascot.minX - gap - bubble.width
        x = clamp(x, screen.minX, screen.maxX - bubble.width)
        let y = clamp(mascot.minY, screen.minY, screen.maxY - bubble.height)
        return (CGRect(x: x, y: y, width: bubble.width, height: bubble.height), side)
    }

    /// `lo` wins when the range is empty (a bubble bigger than the screen keeps its top/left on it).
    private static func clamp(_ v: CGFloat, _ lo: CGFloat, _ hi: CGFloat) -> CGFloat {
        max(lo, min(v, hi))
    }
}
