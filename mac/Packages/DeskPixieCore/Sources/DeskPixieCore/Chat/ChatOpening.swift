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

/// What showing the chat window does (#302), from how it stands when asked. A real open (the
/// window was closed) loads the thread again; a window that comes back from hidden, covered or
/// minimized keeps what it has (events kept it current) and where it was scrolled to. Coming
/// back with something unread opens it again: nothing counts as read until the scroll to the
/// oldest unread has run, so what came while it was away isn't marked read off-screen.
public struct ChatShow: Equatable, Sendable {
    /// The thread is loaded again (only on a real open).
    public let reloads: Bool
    /// Wait for this show's own scroll before anything counts as read (`ChatOpening.opening`).
    public let reopens: Bool
    /// A scroll runs: to the entry asked for, else where the thread opens.
    public let scrolls: Bool

    /// `opening`: the window was closed (or never made). `onScreen`: it is shown and not
    /// covered or minimized. `unread`: the core's count.
    public init(opening: Bool, onScreen: Bool, entryID: String?, unread: Int) {
        if opening {
            (reloads, reopens, scrolls) = (true, true, true)
        } else if onScreen {
            (reloads, reopens, scrolls) = (false, false, true)
        } else {
            reloads = false
            reopens = unread > 0
            scrolls = reopens || entryID != nil
        }
    }
}
