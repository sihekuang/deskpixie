/// A deck closed with its « (#286), for both the badge deck and the reply deck: it stays closed
/// while the pointer is still around Pixie, and hovering opens it again only once the pointer
/// has left that region and come back. Something new arriving opens it as usual.
public struct CollapseLatch: Equatable, Sendable {
    public private(set) var collapsed = false

    public init() {}

    /// The « was pressed.
    public mutating func collapse() { collapsed = true }

    /// The pointer left the region around Pixie, or something new arrived to show.
    public mutating func release() { collapsed = false }

    /// Whether hovering counts: never while collapsed.
    public func hover(_ hovering: Bool) -> Bool { hovering && !collapsed }
}
