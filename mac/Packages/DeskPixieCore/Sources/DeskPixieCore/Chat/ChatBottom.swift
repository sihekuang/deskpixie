import Foundation

/// Whether the chat thread is at its bottom (#316). Worked out from the layout each time it's
/// asked, so it holds from the very first layout, even one no change report ever announced. The
/// one thing kept between reports: while the lazy stack measures its rows the content's height
/// flickers (on macOS 15 between two estimates, 121 pt apart) and the end jumps with it, so a
/// view at the bottom is held there through a change in height and scrolls back. Only a report
/// at the same height, a scroll, lets go of it.
@MainActor
public struct ChatBottom: Equatable, Sendable {
    /// At the bottom through a change in height (or the viewport's), until a scroll says otherwise.
    public private(set) var held = false

    public init() {}

    public func atBottom(sentinelMaxY: Double, viewportHeight: Double) -> Bool {
        held || ChatThreadModel.isAtBottom(sentinelMaxY: sentinelMaxY, viewportHeight: viewportHeight)
    }

    /// A layout report, from `old` to `new`. `following`: the thread is open and its opening
    /// scroll's layout is in; before that, only where the end is counts. True: scroll to the
    /// bottom.
    public mutating func laidOut(oldMaxY: Double, oldHeight: Double, newMaxY: Double, newHeight: Double,
                                 viewportHeight: Double, following: Bool) -> Bool {
        let was = atBottom(sentinelMaxY: oldMaxY, viewportHeight: viewportHeight)
        held = following && ChatThreadModel.keepsBottom(wasAtBottom: was, heightChanged: newHeight != oldHeight)
        return held && !ChatThreadModel.isAtBottom(sentinelMaxY: newMaxY, viewportHeight: viewportHeight)
    }

    /// The viewport changed height (the box under the thread grew or shrank, the window was
    /// resized). True: it was at the bottom, so it stays there; scroll to it.
    public mutating func resized(sentinelMaxY: Double, oldViewport: Double, newViewport: Double,
                                 following: Bool) -> Bool {
        let was = atBottom(sentinelMaxY: sentinelMaxY, viewportHeight: oldViewport)
        held = following && ChatThreadModel.staysAtBottom(wasAtBottom: was, oldViewport: oldViewport,
                                                          newViewport: newViewport)
        return held
    }

    /// The layout after the opening scroll: from here, only where the end is counts.
    public mutating func settled() { held = false }

    // MARK: Landing

    /// How many times an opening scroll to the end is tried again.
    public static let landingTries = 3
    private var triesLeft = 0

    /// The opening scroll went to the end (true) or to a message (false), from where the end is
    /// now. On macOS 15, opened from Pixie's button with DeskPixie in the background, it can run
    /// without moving anything, and the window came up at the top under "↓ Latest". Already at
    /// the end (shown again after a hide), there is nothing to land: no report may come to end
    /// it, and a scroll up later is yours.
    public mutating func aimed(atBottom: Bool, sentinelMaxY: Double, viewportHeight: Double) {
        let there = ChatThreadModel.isAtBottom(sentinelMaxY: sentinelMaxY, viewportHeight: viewportHeight)
        triesLeft = atBottom && !there ? Self.landingTries : 0
    }

    /// A layout report while an opening scroll to the end lands. True: it isn't there, scroll
    /// again. A report before the viewport has its height decides nothing.
    public mutating func landed(sentinelMaxY: Double, viewportHeight: Double) -> Bool {
        guard triesLeft > 0, viewportHeight > 0 else { return false }
        if ChatThreadModel.isAtBottom(sentinelMaxY: sentinelMaxY, viewportHeight: viewportHeight) {
            triesLeft = 0
            return false
        }
        triesLeft -= 1
        return true
    }
}
