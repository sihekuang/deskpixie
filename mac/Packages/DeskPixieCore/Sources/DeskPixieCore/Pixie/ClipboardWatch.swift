import Foundation

/// Notices new copies (#240): the pasteboard has no change notification, so the shell polls its
/// change count. True once per new copy of an image or files, never for what was there at launch.
public struct ClipboardWatch: Equatable, Sendable {
    private var last: Int?

    public init() {}

    public mutating func poll(changeCount: Int, kind: ClipboardKind) -> Bool {
        defer { last = changeCount }
        guard let last, changeCount != last else { return false }
        return kind != .none
    }
}

/// Pixie's "Want me to take this?" bubble: up for a few seconds, longer while the pointer is on it.
public struct CopyOffer: Equatable, Sendable {
    public static let showFor: TimeInterval = 5
    public private(set) var kind: ClipboardKind?
    private var since: Date?

    public init() {}

    public mutating func show(_ kind: ClipboardKind, now: Date) {
        self.kind = kind
        since = now
    }

    /// The bubble was clicked (or the copy attached another way).
    public mutating func take() {
        kind = nil
        since = nil
    }

    /// Puts it away once its time is up and the pointer isn't on it; true when it did. The
    /// pointer leaving starts no new wait.
    public mutating func expire(now: Date, hovering: Bool) -> Bool {
        guard kind != nil, let since, !hovering, now.timeIntervalSince(since) > Self.showFor else { return false }
        take()
        return true
    }
}
