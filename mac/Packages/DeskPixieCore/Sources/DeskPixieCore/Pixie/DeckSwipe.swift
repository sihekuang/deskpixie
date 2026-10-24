import CoreGraphics

/// A trackpad swipe as deck steps (#286): one card per gesture, whatever its length.
public struct DeckSwipe: Equatable, Sendable {
    public static let threshold: CGFloat = 30
    public enum Phase: Sendable { case began, changed, ended, momentum }

    private var sumX: CGFloat = 0
    private var sumY: CGFloat = 0
    /// This gesture already stepped: silent until the next `.began`.
    private var fired = false

    public init() {}

    /// −1 (older), +1 (newer) or 0. A vertical-dominant or momentum event steps nothing. Content
    /// follows the fingers, so a swipe to the right (positive `deltaX`) shows the older card.
    public mutating func feed(deltaX: CGFloat, deltaY: CGFloat, phase: Phase) -> Int {
        switch phase {
        case .momentum, .ended:
            return 0
        case .began:
            sumX = 0
            sumY = 0
            fired = false
        case .changed:
            break
        }
        guard !fired else { return 0 }
        sumX += deltaX
        sumY += deltaY
        guard abs(sumX) >= Self.threshold, abs(sumX) > abs(sumY) else { return 0 }
        fired = true
        return sumX > 0 ? -1 : 1
    }
}
