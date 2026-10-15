import CoreGraphics
import Foundation

/// One unheard agent update, as the core's `updates` event lists it (outbox order, oldest first).
public struct UpdateLine: Decodable, Sendable, Equatable, Identifiable {
    /// The outbox id `agent.updatesSeen` takes back.
    public let id: UInt64
    public let text: String
    /// "reply" | "question" | "unsolicited" | "info".
    public let kind: String
    /// Milliseconds since the epoch.
    public let at: Int64
    /// The job the line is about (its herdr pane is the click target); nil for general lines.
    public let job: String?
    /// The worker the line is about, when it is about one.
    public let worker: String?
    /// The confirm word of a permission question still waiting; `agent.decide` takes it back.
    public let token: String?

    /// Whether the row shows Allow and Deny buttons.
    public var offersDecision: Bool { kind == "question" && token != nil }

    public init(id: UInt64, text: String, kind: String, at: Int64, job: String?, worker: String?,
                token: String? = nil) {
        self.id = id
        self.text = text
        self.kind = kind
        self.at = at
        self.job = job
        self.worker = worker
        self.token = token
    }
}

/// Whether a list that brings new lines may pop the bubble.
public enum PopPermission: Sendable {
    /// Present and nothing in the way: pop.
    case allowed
    /// Present but a pop would intrude (fullscreen app, Pixie hidden): badge only, nothing held.
    case badgeOnly
    /// Away: badge only, and pop once on return (`back`).
    case hold
}

/// The updates bubble's state machine (spec §4): hidden → popped → badge ⇄ expanded. Pure: the
/// window feeds it the core's lists, the pointer and a clock, and draws `phase` / `shown`.
/// Times are seconds on one clock.
public struct UpdatesBubbleModel: Sendable {
    public enum Phase: Equatable, Sendable {
        case hidden
        /// Showing the latest lines until `until`, then the badge.
        case popped(until: TimeInterval)
        /// Only the count.
        case badge
        /// Open under the pointer since `since` (≥ 1 s marks the shown lines seen).
        case expanded(since: TimeInterval)
    }

    /// How long the open-under-the-pointer bubble takes to count as seen (fixed, not a setting).
    public static let seenAfter: TimeInterval = 1

    /// Below this opacity the bubble is too faded to count as looked at.
    public static let hoverMinAlpha: CGFloat = 0.1

    /// Whether the pointer over the panel counts as hovering the bubble: only while it can be
    /// seen (a badge faded with Pixie is not opened or marked seen by a passing pointer). Once
    /// open under the pointer it is drawn opaque, so it stays open.
    public static func hoverCounts(pointerOnPanel: Bool, alpha: CGFloat, alreadyInside: Bool) -> Bool {
        pointerOnPanel && (alreadyInside || alpha >= hoverMinAlpha)
    }

    /// `notifications.bubble.popSecs` (live).
    public var popSecs: TimeInterval
    /// `notifications.bubble.lines` (live).
    public var maxLines: Int

    public private(set) var phase: Phase = .hidden
    /// The core's list, oldest first.
    public private(set) var lines: [UpdateLine] = []

    /// What the expanded bubble shows, newest first: the list as it was on opening plus lines
    /// added since. The core drops seen lines after the 1 s call; the open bubble keeps them
    /// until the pointer leaves.
    private var frozen: [UpdateLine]?
    /// While expanded: when each shown line was first shown, and which were already reported.
    private var shownSince: [UInt64: TimeInterval] = [:]
    private var seenSent: Set<UInt64> = []
    /// Something arrived while away: pop once on return.
    private var heldPop = false
    /// The first list after launch is the restored outbox: badge, never a pop.
    private var applied = false

    public init(popSecs: TimeInterval = 6, maxLines: Int = 5) {
        self.popSecs = popSecs
        self.maxLines = maxLines
    }

    /// The badge's number: unheard lines in the core's list.
    public var count: Int { lines.count }

    /// Newest first, at most `maxLines`.
    public var shown: [UpdateLine] { Array(display.prefix(max(0, maxLines))) }

    /// The "+N more" row's N.
    public var more: Int { max(0, display.count - max(0, maxLines)) }

    private var display: [UpdateLine] { frozen ?? lines.reversed() }

    private var isExpanded: Bool {
        if case .expanded = phase { return true }
        return false
    }

    private var isPopped: Bool {
        if case .popped = phase { return true }
        return false
    }

    /// The core sent its current list (`updates` event).
    public mutating func apply(_ new: [UpdateLine], now: TimeInterval, pop: PopPermission) {
        let old = Set(lines.map(\.id))
        let added = new.filter { !old.contains($0.id) }
        lines = new
        if isExpanded, !added.isEmpty {
            frozen = added.reversed() + (frozen ?? [])
            noteShown(now)
        }

        if !applied {
            applied = true
            if !isExpanded { phase = lines.isEmpty ? .hidden : .badge }
            return
        }
        if lines.isEmpty {
            heldPop = false
            if !isExpanded { phase = .hidden }
            return
        }
        if !added.isEmpty {
            switch pop {
            case .allowed:
                if !isExpanded { phase = .popped(until: now + popSecs) }
            case .hold:
                heldPop = true
                if !isExpanded { phase = .badge }
            case .badgeOnly:
                if !isExpanded, !isPopped { phase = .badge }
            }
            return
        }
        if phase == .hidden { phase = .badge }
    }

    /// The pointer entered the bubble or badge (a click also lands here): open it.
    public mutating func pointerEntered(now: TimeInterval) {
        guard !lines.isEmpty || isPopped else { return }
        phase = .expanded(since: now)
        frozen = lines.reversed()
        shownSince = [:]
        seenSent = []
        noteShown(now)
    }

    /// The pointer left: back to the badge, or hidden when nothing unheard is left.
    public mutating func pointerLeft() {
        frozen = nil
        shownSince = [:]
        seenSent = []
        phase = lines.isEmpty ? .hidden : .badge
    }

    /// Advances timers. Returns the ids to send to `agent.updatesSeen`: each shown line once per
    /// expansion, after it has been shown `seenAfter` (lines added while open count from when
    /// they appeared).
    public mutating func tick(now: TimeInterval) -> [UInt64] {
        switch phase {
        case .popped(let until) where now >= until:
            phase = .badge
        case .expanded:
            noteShown(now)
            let due = shown.map(\.id).filter { id in
                !seenSent.contains(id) && now - (shownSince[id] ?? now) >= Self.seenAfter
            }
            seenSent.formUnion(due)
            return due
        default:
            break
        }
        return []
    }

    /// Stamps the lines shown now that weren't shown before in this expansion.
    private mutating func noteShown(_ now: TimeInterval) {
        for l in shown where shownSince[l.id] == nil {
            shownSince[l.id] = now
        }
    }

    /// Presence came back: one pop if anything arrived while away.
    public mutating func back(now: TimeInterval, pop: PopPermission) {
        if heldPop, !lines.isEmpty, pop == .allowed, !isExpanded {
            phase = .popped(until: now + popSecs)
        }
        heldPop = false
    }
}
