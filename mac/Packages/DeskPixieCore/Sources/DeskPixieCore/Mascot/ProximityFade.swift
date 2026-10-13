import CoreGraphics

/// Fades Bit as the cursor approaches, so he never blocks what is underneath.
public enum ProximityFade {
    /// Bit never fully vanishes.
    public static let minOpacity: CGFloat = 0.15

    // Borrowed from oled-saver-macos BallPhysics.proximityOpacity (MIT, same author), adapted to a rect.
    /// Distance is from the cursor to the nearest point of `spriteFrame` (0 inside). Returns 1.0 at
    /// >= `fadeRadius`, `minOpacity` at <= 30% of `fadeRadius`, quadratic in between (floored).
    public static func opacity(spriteFrame: CGRect, cursor: CGPoint, fadeRadius: CGFloat, enabled: Bool) -> CGFloat {
        guard enabled, fadeRadius > 0 else { return 1.0 }

        let dx = max(spriteFrame.minX - cursor.x, 0, cursor.x - spriteFrame.maxX)
        let dy = max(spriteFrame.minY - cursor.y, 0, cursor.y - spriteFrame.maxY)
        let distFromEdge = (dx * dx + dy * dy).squareRoot()

        // Fade reaches its floor at innerCutoff (30% of fadeRadius), not at the edge.
        let innerCutoff = fadeRadius * 0.3
        if distFromEdge >= fadeRadius { return 1.0 }
        if distFromEdge <= innerCutoff { return minOpacity }

        let linear = (distFromEdge - innerCutoff) / (fadeRadius - innerCutoff)
        return max(linear * linear, minOpacity) // Quadratic fade
    }
}
