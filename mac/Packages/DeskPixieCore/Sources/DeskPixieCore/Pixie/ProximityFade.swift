import CoreGraphics

/// Proximity fade, a port of ai-mascot `src-tauri/src/proximity.rs` (itself oled-saver's
/// `BallPhysics.proximityOpacity`): fully visible beyond `radius`, invisible within 30% of it,
/// quadratic between; plus the cursor → sprite cell mapping used for click-through.
/// Coordinates are points in desktop space (`DesktopSpace`: top-left origin, y down; may be
/// negative). Distances do not depend on the y direction; `cursorCell` does.
public enum ProximityFade {
    /// Converts a point scaled by `from` into one scaled by `to` (same logical position).
    /// tao reported the cursor in the primary display's scale and the window in its own; AppKit
    /// reports both in global points, so the app passes equal factors.
    public static func rescale(_ p: CGPoint, from: CGFloat, to: CGFloat) -> CGPoint {
        guard from > 0, to > 0 else { return p }
        return CGPoint(x: p.x / from * to, y: p.y / from * to)
    }

    /// The sprite cell under the cursor, if the cursor is inside the window rect (right and
    /// bottom edges exclusive). The window shows one cell scaled to fill it (never mirrored), so a
    /// bobbing frame needs no extra mapping: the hit mask already is the union of every frame
    /// and bob offset (P6 / hit_mask.rs).
    public static func cursorCell(_ cursor: CGPoint, rect r: CGRect, cell: (Int, Int)) -> CellIndex? {
        guard r.width > 0, r.height > 0 else { return nil }
        let fx = (cursor.x - r.minX) / r.width, fy = (cursor.y - r.minY) / r.height
        guard (0..<1).contains(fx), (0..<1).contains(fy) else { return nil }
        return CellIndex(x: Int(fx * CGFloat(cell.0)), y: Int(fy * CGFloat(cell.1)))
    }

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

    /// actions.rs `spawn_proximity`: full opacity while the cursor is on Pixie's own pixels
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

/// A pixel inside a sprite cell (top-left origin).
public struct CellIndex: Equatable, Sendable {
    public let x, y: Int

    public init(x: Int, y: Int) {
        self.x = x
        self.y = y
    }
}
