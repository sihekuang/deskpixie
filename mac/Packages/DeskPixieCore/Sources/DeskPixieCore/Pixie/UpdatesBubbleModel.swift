import CoreGraphics
import Foundation

/// One unheard agent update, as the core's `updates` event lists it (outbox order, oldest first).
public struct UpdateLine: Codable, Sendable, Equatable, Identifiable {
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
    /// A session menu's option descriptions, "" for none (#268).
    public let details: [String]?
    /// The menu also takes a typed answer (`agent.typedAnswer`) and "Chat about this"
    /// (`agent.chatAbout`), as its terminal does (#268).
    public let typed: Bool?
    /// You tick several of the menu's options and send them (`agent.pick`, #364).
    public let multi: Bool?
    /// A session's own question with no buttons: Dismiss takes it off the badge
    /// (`agent.dismiss`, #388); the session still waits for an answer in its terminal.
    public let dismissible: Bool?

    /// What the row says: the brief when there is one, else the full text.
    public var shownText: String {
        if let brief, !brief.isEmpty { return brief }
        return text
    }

    /// Whether the row shows Allow and Deny buttons.
    public var offersDecision: Bool { kind == "question" && token != nil && (options ?? []).isEmpty }

    /// Whether the row shows one button per option (a session's menu, #105).
    public var offersOptions: Bool { kind == "question" && token != nil && !(options ?? []).isEmpty }

    /// Whether the menu also shows "Type something…" and "Chat about this" (#268).
    public var offersTyped: Bool { offersOptions && typed == true && multi != true }

    /// Whether its options are boxes to tick, sent together (#364).
    public var offersPicks: Bool { offersOptions && multi == true }

    /// The line asks you something but offers no buttons (#286): its card offers its terminal.
    public var offersTerminal: Bool {
        !offersDecision && !offersOptions && (kind == "question" || asking == true)
    }

    /// Whether the card shows Dismiss (#388).
    public var offersDismiss: Bool { offersTerminal && dismissible == true }

    /// The card says where it comes from (a session, a job, a worker or an app): its terminal
    /// can be brought up, whatever buttons it has (#388).
    public var hasSource: Bool {
        [session, job, worker, app].contains { !($0 ?? "").isEmpty }
    }

    /// Its entry in the chat window: the thread keys a waiting request by its line (#388).
    public var chatEntryID: String { "req\(id)" }

    /// The badge card's header (#286): "Permission · <who>", "<who> is asking", else who it is
    /// about.
    public var deckHeader: String {
        let who = title.flatMap { $0.isEmpty ? nil : $0 }
        if offersDecision { return who.map { "Permission · \($0)" } ?? "Permission" }
        if kind == "question" || asking == true { return who.map { "\($0) is asking" } ?? "Question" }
        return who ?? "Update"
    }

    /// Option i's description, or nil when it has none.
    public func detail(of i: Int) -> String? {
        guard let details, details.indices.contains(i), !details[i].isEmpty else { return nil }
        return details[i]
    }

    public init(id: UInt64, text: String, kind: String, at: Int64, job: String?, worker: String?,
                token: String? = nil, app: String? = nil, session: String? = nil,
                title: String? = nil, brief: String? = nil, asking: Bool? = nil,
                options: [String]? = nil, note: String? = nil, details: [String]? = nil, typed: Bool? = nil,
                multi: Bool? = nil, dismissible: Bool? = nil) {
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
        self.details = details
        self.typed = typed
        self.multi = multi
        self.dismissible = dismissible
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
        /// Open under the pointer since `since`. Looking marks nothing seen: a line leaves when
        /// it is clicked or cleared (#198), heard in a conversation, or answered.
        case expanded(since: TimeInterval)
    }

    /// How long Allow and Deny ignore taps after a question's buttons appear by themselves (#97),
    /// so a click landing as the bubble pops never approves.
    public static let decisionGuard: TimeInterval = 0.75

    /// Below this opacity the bubble is too faded to count as looked at.
    public static let hoverMinAlpha: CGFloat = 0.1

    /// Whether the pointer over the panel counts as hovering the bubble: only while it can be
    /// seen (a badge faded with Pixie is not opened by a passing pointer). Once
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
    /// The badge deck's card (#286): an index into `lines`, oldest first.
    public private(set) var deckIndex = 0

    /// What the expanded bubble shows, newest first: the list as it was on opening plus lines
    /// added since, so rows don't move under the pointer: a line the core drops (answered or
    /// heard elsewhere) stays until the pointer leaves.
    private var frozen: [UpdateLine]?
    /// Something arrived while away: pop once on return.
    private var heldPop = false
    /// The first list after launch is the restored outbox: badge, never a pop.
    private var applied = false
    /// When each question's Allow and Deny (by token) appeared; only while they are showing.
    private var decisionsSince: [String: TimeInterval] = [:]
    /// Where the pointer opened the bubble, and the questions whose buttons came with it (#268).
    private var openedAt: CGPoint?
    private var hoverOpened: Set<String> = []
    /// Lines clicked away (#194): left out of the core's lists until it drops them too.
    private var dismissed: Set<UInt64> = []
    /// The menu (by token) whose answer you are typing in the bubble (#268): it stays open.
    public private(set) var typing: String?
    /// The pointer left while you typed: the bubble closes once the typing ends.
    private var leftWhileTyping = false

    public init(popSecs: TimeInterval = 6, maxLines: Int = 5) {
        self.popSecs = popSecs
        self.maxLines = maxLines
    }

    /// The badge's number: unheard lines in the core's list.
    public var count: Int { lines.count }

    /// Newest first, at most `maxLines`.
    public var shown: [UpdateLine] { Array(display.prefix(max(0, maxLines))) }

    /// The badge deck's card, nil when nothing is unheard.
    public var current: UpdateLine? { lines.indices.contains(deckIndex) ? lines[deckIndex] : nil }

    /// Where the deck is, counting from 1 ("1 of 1" alone); nil with nothing in it.
    public var deckPosition: (index: Int, count: Int)? {
        lines.isEmpty ? nil : (deckIndex + 1, lines.count)
    }

    /// Moves the deck by `delta` cards (+1 newer, −1 older), stopping at either end.
    /// With `now`, the card that comes up has its buttons stamped then (the stray-click guard).
    public mutating func step(_ delta: Int, now: TimeInterval? = nil) {
        deckIndex = min(max(0, deckIndex + delta), max(0, lines.count - 1))
        if let now { noteDecisions(now) }
    }

    /// Scrolled to `id`'s card (#286); nothing when it isn't listed.
    public mutating func show(id: UInt64, now: TimeInterval? = nil) {
        if let i = lines.firstIndex(where: { $0.id == id }) { deckIndex = i }
        if let now { noteDecisions(now) }
    }

    /// Keeps the deck on `id`'s card if it is still listed, else on the nearest one.
    private mutating func settleDeck(on id: UInt64?) {
        if let id, let i = lines.firstIndex(where: { $0.id == id }) {
            deckIndex = i
        } else {
            deckIndex = min(max(0, deckIndex), max(0, lines.count - 1))
        }
    }

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
        let typingEnded = typing.map { t in !new.contains(where: { $0.token == t && $0.offersTyped }) } ?? false
        let old = Set(lines.map(\.id))
        let added = new.filter { !old.contains($0.id) }
        let onCard = current?.id
        lines = new
        settleDeck(on: onCard)
        if typingEnded { // answered elsewhere, or its wait ran out
            typing = nil
            if leftWhileTyping { pointerLeft() }
        }
        if isExpanded, !added.isEmpty {
            frozen = added.reversed() + (frozen ?? [])
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
                if !isExpanded {
                    phase = .popped(until: now + popSecs)
                    latch.release()
                }
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

    /// The pointer entered the bubble or badge at `at` (a click also lands here): open it.
    public mutating func pointerEntered(now: TimeInterval, at: CGPoint? = nil) {
        guard !collapsed, !lines.isEmpty || isPopped else { return }
        leftWhileTyping = false
        phase = .expanded(since: now)
        frozen = lines.reversed()
        let before = Set(decisionsSince.keys)
        noteDecisions(now)
        // Opened by you: buttons that came with it take your tap at once once you moved to them
        // (#268). Those already showing (a pop) and those arriving later stay guarded.
        openedAt = at
        hoverOpened = Set(decisionsSince.keys).subtracting(before)
    }

    /// "Type something…" on the menu with token: the bubble holds a field for its answer and
    /// stays open until it is sent or put away. False when that menu takes no typed answer.
    public mutating func startTyping(token: String) -> Bool {
        guard display.contains(where: { $0.token == token && $0.offersTyped }) else { return false }
        typing = token
        return true
    }

    /// The typed answer was sent or put away (Esc, a click elsewhere).
    public mutating func stopTyping(pointerInside: Bool = false) {
        guard typing != nil else { return }
        typing = nil
        if !pointerInside || leftWhileTyping { pointerLeft() }
    }

    /// The deck was closed to the badge with Hide (#286): everything stays waiting, and the
    /// pointer, still around, doesn't open it again until it has left (`regionLeft`). A new item
    /// may still pop it.
    public var collapsed: Bool { latch.collapsed }
    private var latch = CollapseLatch()

    public mutating func collapse() {
        typing = nil
        leftWhileTyping = false
        latch.collapse()
        frozen = nil
        phase = lines.isEmpty ? .hidden : .badge
        decisionsSince = [:]
        hoverOpened = []
    }

    /// The pointer left the region around Pixie: hovering the badge opens the deck again.
    public mutating func regionLeft() {
        latch.release()
    }

    /// The pointer left: back to the badge, or hidden when nothing unheard is left. Not while you
    /// type an answer in it.
    public mutating func pointerLeft() {
        if typing != nil {
            leftWhileTyping = true
            return
        }
        leftWhileTyping = false
        frozen = nil
        phase = lines.isEmpty ? .hidden : .badge
        decisionsSince = [:]
        hoverOpened = []
    }

    /// Advances timers: a pop runs out to the badge (unless a question waits on you).
    /// `holding`: you're around (#286), so a pop doesn't run out yet.
    public mutating func tick(now: TimeInterval, holding: Bool = false) {
        defer { noteDecisions(now) }
        if case .popped(let until) = phase, now >= until, !waitsOnYou, !holding {
            phase = .badge
        }
    }

    /// A click on a line (#194), or on its notification (#199): it leaves the bubble, so the
    /// caller sends its id to `agent.updatesSeen` as clicked. A question with buttons stays until
    /// it is answered; a job's question goes (#199: the job still waits for its answer). Returns
    /// whether the line was dismissed.
    public mutating func dismiss(_ id: UInt64) -> Bool {
        guard let line = display.first(where: { $0.id == id }),
              !line.offersDecision, !line.offersOptions else { return false }
        dismissed.insert(id)
        let onCard = current?.id
        lines.removeAll { $0.id == id }
        settleDeck(on: onCard)
        frozen?.removeAll { $0.id == id }
        if display.isEmpty {
            frozen = nil
            phase = .hidden
        }
        return true
    }

    /// You acted on a card (#286): Allow, Deny, an option, a typed answer or "Chat about this".
    /// It leaves the deck at once and the next slides into its place, without waiting for the
    /// core; a list from the core that still has it doesn't bring it back, until the core drops
    /// it too. A card that asks without buttons stays (#347): opening its terminal doesn't
    /// answer it, and the core drops it once it is answered there.
    public mutating func act(on id: UInt64, now: TimeInterval? = nil) {
        guard let line = lines.first(where: { $0.id == id }), !line.offersTerminal else { return }
        defer { if let now { noteDecisions(now) } }
        dismissed.insert(id)
        let onCard = current?.id
        lines.removeAll { $0.id == id }
        settleDeck(on: onCard == id ? nil : onCard)
        frozen?.removeAll { $0.id == id }
        if lines.isEmpty {
            frozen = nil
            phase = .hidden
        }
    }

    /// A notification's line (#199) that no longer matches the line listed under its id: ids can
    /// repeat across restarts, so an old notification must not dismiss a newer line.
    public func isStale(_ line: UpdateLine) -> Bool {
        guard let listed = (display + lines).first(where: { $0.id == line.id }) else { return false }
        return listed.at != line.at || listed.text != line.text
    }

    /// Presence came back: one pop if anything arrived while away.
    public mutating func back(now: TimeInterval, pop: PopPermission) {
        if heldPop, !lines.isEmpty, pop == .allowed, !isExpanded {
            phase = .popped(until: now + popSecs)
        }
        heldPop = false
        noteDecisions(now)
    }

    /// Whether a tap at `at` on Allow or Deny for this question counts: once its buttons have been
    /// showing `decisionGuard`, or at once on buttons you hovered the bubble open onto and then
    /// moved to (a tap on buttons not showing never counts).
    public mutating func acceptsDecision(token: String, now: TimeInterval, at: CGPoint? = nil) -> Bool {
        noteDecisions(now)
        guard let since = decisionsSince[token] else { return false }
        if now - since >= Self.decisionGuard { return true }
        guard hoverOpened.contains(token), let from = openedAt, let at else { return false }
        return hypot(at.x - from.x, at.y - from.y) > Self.movedDistance
    }

    /// How far the pointer moves after opening the bubble for a tap to count as aimed (points).
    public static let movedDistance: CGFloat = 4

    /// Stamps the question whose buttons show now: the deck's card while the bubble is open
    /// (#286), the only one on screen. The rest are forgotten, so buttons that come back (a step,
    /// or the next card sliding up once one is answered) are guarded again.
    private mutating func noteDecisions(_ now: TimeInterval) {
        guard isExpanded || isPopped else {
            decisionsSince = [:]
            hoverOpened = []
            return
        }
        let showing = Set([current].compactMap { $0 }
            .filter { $0.offersDecision || $0.offersOptions }.compactMap(\.token))
        decisionsSince = decisionsSince.filter { showing.contains($0.key) }
        hoverOpened.formIntersection(showing)
        for token in showing where decisionsSince[token] == nil {
            decisionsSince[token] = now
        }
    }
}
