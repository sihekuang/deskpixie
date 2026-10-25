import Foundation

/// The chat window's opening (#302): until the opening scroll has run, nothing counts as read.
/// A scroll asked for during this open is pending until it runs; one asked for in an earlier
/// open never is, so a view made for a new open can't run the last open's scroll.
public struct ChatOpening: Equatable, Sendable {
    /// The opening scroll has run since the window last opened.
    public private(set) var opened = false
    /// A scroll was asked for in this open and hasn't run yet.
    public private(set) var scrollPending = false

    public init() {}

    /// The window is opening: wait for this open's own scroll.
    public mutating func opening() {
        opened = false
        scrollPending = false
    }

    /// A scroll was asked for.
    public mutating func asked() { scrollPending = true }

    /// A scroll ran: the window is open.
    public mutating func scrolled() {
        scrollPending = false
        opened = true
    }

    /// A view appearing runs the opening scroll only when this open asked for it before the
    /// view could see the request.
    public var scrollsOnAppear: Bool { !opened && scrollPending }
}
