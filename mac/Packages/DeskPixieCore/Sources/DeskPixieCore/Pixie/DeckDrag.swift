import CoreGraphics

/// A two-finger swipe on a deck (#286), as the deck follows it: the cards move with the fingers
/// up to one card either way, and when the fingers lift the deck goes to the neighbour or back.
/// Momentum is swallowed, so a flick never carries it further. A gesture that starts vertical
/// is left alone (a long card's text scrolls), and so is a mouse wheel.
public struct DeckDrag: Equatable, Sendable {
    /// What the window does with a scroll event.
    public enum Verdict: Equatable, Sendable {
        /// Not the deck's: let it through (to the text under it).
        case pass
        /// The deck's: the deck follows `offset`; the event stops here.
        case take
        /// The fingers lifted: go `step` cards (−1 earlier, 0, +1 later); the event stops here.
        case settle(step: Int)
    }

    public enum Phase: Sendable { case began, changed, ended, momentum }

    /// Below this (points) a swipe is a nudge: the deck goes back to its card.
    public static let threshold: CGFloat = 24
    /// How far (points) the fingers go before the gesture counts as sideways or up and down.
    public static let decideAfter: CGFloat = 4
    /// Past the first or last card the deck gives only this much of the fingers' travel.
    public static let resistance: CGFloat = 0.3

    /// How far the cards have moved with the fingers (+ to the right, showing earlier cards).
    public private(set) var offset: CGFloat = 0
    /// A sideways gesture is under way, or its momentum may still come (until the next gesture).
    public private(set) var active = false
    /// The deck follows the fingers: from the moment the gesture counts as sideways until it
    /// settles. Nothing hides meanwhile.
    public private(set) var swiping = false

    private var tracking = false
    private var horizontal: Bool?
    private var sumX: CGFloat = 0
    private var sumY: CGFloat = 0

    public init() {}

    /// One scroll event of a trackpad gesture (`dx`, `dy` as the fingers moved; content follows
    /// them). `stride` is a card plus the gap; `canBack`/`canForward` say whether there is an
    /// earlier or later card to go to.
    public mutating func feed(_ phase: Phase, dx: CGFloat, dy: CGFloat, stride: CGFloat,
                              canBack: Bool, canForward: Bool) -> Verdict {
        switch phase {
        case .began:
            tracking = true
            active = false
            horizontal = nil
            sumX = 0
            sumY = 0
            offset = 0
            return .pass
        case .momentum:
            return active ? .take : .pass
        case .ended:
            let was = tracking && horizontal == true
            tracking = false
            swiping = false
            guard was else { return .pass }
            let moved = -offset
            let step = abs(moved) < Self.threshold ? 0 : (moved > 0 ? 1 : -1)
            offset = 0
            // Momentum may still come: it stays swallowed until the next gesture begins.
            return .settle(step: (step < 0 && !canBack) || (step > 0 && !canForward) ? 0 : step)
        case .changed:
            guard tracking else { return .pass }
            if horizontal == nil {
                sumX += dx
                sumY += dy
                guard max(abs(sumX), abs(sumY)) >= Self.decideAfter else { return .pass }
                horizontal = abs(sumX) > abs(sumY)
                active = horizontal == true
                swiping = active
                guard active else { return .pass }
                return move(sumX, stride: stride, canBack: canBack, canForward: canForward)
            }
            guard horizontal == true else { return .pass }
            return move(dx, stride: stride, canBack: canBack, canForward: canForward)
        }
    }

    /// The deck went away mid-swipe: forget the gesture. True when a swipe was under way, so its
    /// end is still owed to whoever was told it started.
    public mutating func cancel() -> Bool {
        let was = swiping
        self = DeckDrag()
        return was
    }

    private mutating func move(_ dx: CGFloat, stride: CGFloat, canBack: Bool,
                               canForward: Bool) -> Verdict {
        var d = dx
        // Towards a card that isn't there (+ shows earlier cards): it resists.
        if (offset + d > 0 && !canBack) || (offset + d < 0 && !canForward) { d *= Self.resistance }
        offset = min(max(offset + d, -stride), stride)
        return .take
    }
}
