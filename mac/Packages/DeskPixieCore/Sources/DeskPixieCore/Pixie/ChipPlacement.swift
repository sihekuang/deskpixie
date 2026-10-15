import CoreGraphics

/// Where the roster chip (and the list it opens) sits: centred under Pixie, clamped on screen.
/// Desktop coordinates (`DesktopSpace`: top-left origin, y down), like `BubblePlacement`.
public enum ChipPlacement {
    /// `panel` is the chip, or the chip with the open list. `openHeight` (the open panel's height)
    /// picks below/above in every phase, so the chip doesn't jump when the list opens. Above
    /// Pixie when the open panel wouldn't fit below.
    public static func frame(pixie: CGRect, panel: CGSize, screen: CGRect, openHeight: CGFloat,
                             gap: CGFloat = 4) -> (CGRect, below: Bool) {
        let below = pixie.maxY + gap + max(openHeight, panel.height) <= screen.maxY
        let x = clamp(pixie.midX - panel.width / 2, screen.minX, screen.maxX - panel.width)
        let y = clamp(below ? pixie.maxY + gap : pixie.minY - gap - panel.height,
                      screen.minY, screen.maxY - panel.height)
        return (CGRect(x: x, y: y, width: panel.width, height: panel.height), below)
    }

    /// `lo` wins when the range is empty.
    private static func clamp(_ v: CGFloat, _ lo: CGFloat, _ hi: CGFloat) -> CGFloat { max(lo, min(v, hi)) }
}
