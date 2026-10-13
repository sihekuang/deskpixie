import CoreGraphics

/// Proximity fade, a port of ai-mascot `src-tauri/src/proximity.rs` (itself oled-saver's
/// `BallPhysics.proximityOpacity`): fully visible beyond `radius`, invisible within 30% of it,
/// quadratic between. Coordinates are AppKit points in the global desktop space (may be negative).
public enum ProximityFade {
    /// Distance from `p` to the nearest point of `r`; 0 when inside.
    public static func distanceToRect(_ p: CGPoint, _ r: CGRect) -> CGFloat {
        let dx = max(r.minX - p.x, p.x - r.maxX, 0)
        let dy = max(r.minY - p.y, p.y - r.maxY, 0)
        return (dx * dx + dy * dy).squareRoot()
    }

    public static func opacity(cursor: CGPoint, rect: CGRect, radius: CGFloat) -> CGFloat {
        let d = distanceToRect(cursor, rect)
        if d >= radius { return 1.0 }
        let inner = radius * 0.3
        if d <= inner { return 0.0 }
        let t = (d - inner) / (radius - inner)
        return t * t
    }

    /// actions.rs `spawn_proximity`: full opacity while the cursor is on Bit's own pixels
    /// (`overBit`, which also covers an active press/drag) or when the fade is off.
    public static func target(cursor: CGPoint, rect: CGRect, radius: CGFloat,
                              enabled: Bool, overBit: Bool) -> CGFloat {
        (overBit || !enabled) ? 1.0 : opacity(cursor: cursor, rect: rect, radius: radius)
    }

    /// Skip tiny changes, but always deliver reaching fully hidden or fully visible.
    public static func shouldEmit(last: CGFloat, next: CGFloat) -> Bool {
        if next == last { return false }
        return abs(next - last) >= 0.01 || next == 0.0 || next == 1.0
    }
}
