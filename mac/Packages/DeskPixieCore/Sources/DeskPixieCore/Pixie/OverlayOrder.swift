/// The two panels beside Pixie: the updates bubble and the roster's (the chip row, its list,
/// the text box and the reply card). They can overlap, most of all with a small Pixie, whose
/// chip row is wider than it is (#398).
public enum PixieOverlay: Sendable { case bubble, roster }

/// What each panel shows, and so which goes in front: what you are using over what only
/// shows. The bubble you point at, then the list or text box you opened, then a bubble that
/// popped up by itself (a question waits in it), then a reply card. Neither when nothing
/// overlaps that matters.
public struct OverlayOrder: Equatable, Sendable {
    public var bubbleExpanded = false
    public var bubblePopped = false
    /// The roster's list or the text box is open.
    public var rosterOpen = false
    /// A reply card shows on its own.
    public var rosterCard = false

    public init() {}

    public var front: PixieOverlay? {
        if bubbleExpanded { return .bubble }
        if rosterOpen { return .roster }
        if bubblePopped { return .bubble }
        if rosterCard { return .roster }
        return nil
    }
}
