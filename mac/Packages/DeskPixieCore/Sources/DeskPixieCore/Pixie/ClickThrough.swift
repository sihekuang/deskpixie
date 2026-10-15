import CoreGraphics

/// The 60 Hz cursor poll's decision (actions.rs `spawn_proximity`, V-031), extracted from
/// PixieController: the pixie panel ignores the mouse wherever no frame of Pixie ever draws
/// (the hit mask already is the union of every frame, mouth level, face offset and bob), but
/// never during a press or drag; and the proximity fade's target opacity.
public enum ClickThrough {
    public struct Decision: Equatable, Sendable {
        /// `NSWindow.ignoresMouseEvents` for the panel.
        public var ignoresMouseEvents: Bool
        /// The cursor is over one of Pixie's pixels.
        public var overBit: Bool
        /// A press or drag that started on Pixie is still held.
        public var pointerActive: Bool
        /// ProximityFade target.
        public var opacity: CGFloat
    }

    /// - cursor, window: desktop points (y down); the window shows one cell scaled to fill it,
    ///   so the scale is the window size over the mask size.
    /// - pointerActive: a press on Pixie was seen; it ends once `leftButtonDown` is false.
    /// - mirrored: the cell is drawn flipped horizontally (columns are mirrored).
    public static func decide(cursor: CGPoint, window: CGRect, mask: HitMask, pointerActive: Bool,
                              leftButtonDown: Bool, mirrored: Bool = false, fadeEnabled: Bool,
                              fadeRadius: CGFloat) -> Decision {
        let active = pointerActive && leftButtonDown
        let over = ProximityFade.cursorCell(cursor, rect: window, cell: (mask.width, mask.height)).map {
            mask.contains(x: mirrored ? mask.width - 1 - $0.x : $0.x, y: $0.y)
        } ?? false
        let opacity = ProximityFade.target(cursor: cursor, rect: window, radius: fadeRadius, enabled: fadeEnabled,
                                           overBit: active || over)
        return Decision(ignoresMouseEvents: !(active || over), overBit: over, pointerActive: active, opacity: opacity)
    }
}
