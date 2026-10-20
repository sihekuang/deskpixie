import CoreGraphics
import Foundation

/// One unheard agent update, as the core's `updates` event lists it (outbox order, oldest first).
public struct UpdateLine: Decodable, Sendable, Equatable, Identifiable {
    /// The outbox id `agent.updatesSeen` takes back.
    public let id: UInt64
    public let text: String
    /// "reply" | "question" | "unsolicited" | "info" | "session".
    public let kind: String
    /// Milliseconds since the epoch.
    public let at: Int64
    /// The job the line is about (its herdr pane is the click target); nil for general lines.
    public let job: String?
    /// The worker the line is about, when it is about one.
    public let worker: String?
    /// The confirm word of a permission question still waiting; `agent.decide` takes it back.
    public let token: String?
    /// A session line's app (bundle id): a click activates it instead of focusing a pane (#114).
    public let app: String?
    /// A session line's session (its name): a click brings up the tab it runs in (#194).
    public let session: String?
    /// The bubble's short form (#197): who the line is about, on its own line, and the news.
    /// Either may be missing: the bubble then shows `text`.
    public let title: String?
    public let brief: String?
    /// The line asks you something.
    public let asking: Bool?
    /// A session menu's choices (#105): one button each, answered with `agent.choose`.
    public let options: [String]?
    /// Shown small under the line, never said: a session dialog's command (#105).
    public let note: String?

    /// What the row says: the brief when there is one, else the full text.
    public var shownText: String {
        if let brief, !brief.isEmpty { return brief }
        return text
    }

    /// Whether the row shows Allow and Deny buttons.
    public var offersDecision: Bool { kind == "question" && token != nil && (options ?? []).isEmpty }

    /// Whether the row shows one button per option (a session's menu, #105).
    public var offersOptions: Bool { kind == "question" && token != nil && !(options ?? []).isEmpty }

    public init(id: UInt64, text: String, kind: String, at: Int64, job: String?, worker: String?,
                token: String? = nil, app: String? = nil, session: String? = nil,
                title: String? = nil, brief: String? = nil, asking: Bool? = nil,
                options: [String]? = nil, note: String? = nil) {
        self.id = id
        self.text = text
        self.kind = kind
        self.at = at
        self.job = job
        self.worker = worker
        self.token = token
        self.app = app
        self.session = session
        self.title = title
        self.brief = brief
        self.asking = asking
        self.options = options
        self.note = note
    }
}

/// How long ago a line came, as the bubble shows it (#197): "now", "4m ago", "2h ago", "3d ago".
public func updateAge(seconds: TimeInterval) -> String {
    let s = Int(max(0, seconds))
    switch s {
    case ..<60: return "now"
    case ..<3600: return "\(s / 60)m ago"
    case ..<86400: return "\(s / 3600)h ago"
    default: return "\(s / 86400)d ago"
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

    /// How long Allow and Deny ignore taps after a question's buttons appear (#97), so a click
    /// landing as the bubble pops never approves.
    public static let decisionGuard: TimeInterval = 0.75

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
    /// When each question's Allow and Deny (by token) appeared; only while they are showing.
    private var decisionsSince: [String: TimeInterval] = [:]
    /// Lines clicked away (#194): left out of the core's lists until it drops them too.
    private var dismissed: Set<UInt64> = []

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

    /// A question with buttons is waiting on your answer: a popped bubble stays open (#105).
    private var waitsOnYou: Bool { lines.contains { $0.offersDecision || $0.offersOptions } }

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
        defer { noteDecisions(now) }
        let ids = Set(new.map(\.id))
        dismissed = dismissed.filter { ids.contains($0) }
        let new = new.filter { !dismissed.contains($0.id) }
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
        noteDecisions(now)
    }

    /// The pointer left: back to the badge, or hidden when nothing unheard is left.
    public mutating func pointerLeft() {
        frozen = nil
        shownSince = [:]
        seenSent = []
        phase = lines.isEmpty ? .hidden : .badge
        decisionsSince = [:]
    }

    /// Advances timers. Returns the ids to send to `agent.updatesSeen`: each shown line once per
    /// expansion, after it has been shown `seenAfter` (lines added while open count from when
    /// they appeared).
    public mutating func tick(now: TimeInterval) -> [UInt64] {
        defer { noteDecisions(now) }
        switch phase {
        case .popped(let until) where now >= until && !waitsOnYou:
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

    /// A click on a line (#194): it leaves the bubble and counts as seen, so the caller sends its
    /// id to `agent.updatesSeen`. A question with buttons stays until it is answered. Returns
    /// whether the line was dismissed.
    public mutating func dismiss(_ id: UInt64) -> Bool {
        guard let line = display.first(where: { $0.id == id }),
              !line.offersDecision, !line.offersOptions else { return false }
        dismissed.insert(id)
        lines.removeAll { $0.id == id }
        frozen?.removeAll { $0.id == id }
        seenSent.insert(id)
        if display.isEmpty {
            frozen = nil
            phase = .hidden
        }
        return true
    }

    /// Presence came back: one pop if anything arrived while away.
    public mutating func back(now: TimeInterval, pop: PopPermission) {
        if heldPop, !lines.isEmpty, pop == .allowed, !isExpanded {
            phase = .popped(until: now + popSecs)
        }
        heldPop = false
        noteDecisions(now)
    }

    /// Whether a tap on Allow or Deny for this question counts: only once its buttons have been
    /// showing `decisionGuard` (a tap on buttons not showing never counts).
    public mutating func acceptsDecision(token: String, now: TimeInterval) -> Bool {
        noteDecisions(now)
        guard let since = decisionsSince[token] else { return false }
        return now - since >= Self.decisionGuard
    }

    /// Stamps the questions whose Allow and Deny show now (the bubble is open and the row is
    /// listed) and forgets the rest, so buttons that reappear are guarded again.
    private mutating func noteDecisions(_ now: TimeInterval) {
        guard isExpanded || isPopped else {
            decisionsSince = [:]
            return
        }
        let showing = Set(shown.filter { $0.offersDecision || $0.offersOptions }.compactMap(\.token))
        decisionsSince = decisionsSince.filter { showing.contains($0.key) }
        for token in showing where decisionsSince[token] == nil {
            decisionsSince[token] = now
        }
    }
}
