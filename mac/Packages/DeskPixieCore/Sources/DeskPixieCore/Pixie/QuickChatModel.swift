import Foundation

/// One spoken line for a message typed in the text box, as the core's `chat.line` event sends
/// it (#221). `status`: working | question | done | failed.
public struct ChatLine: Decodable, Sendable, Equatable {
    public let job: String
    public let status: String
    public let text: String
    /// The sessions the line is about (#261): the card's buttons that go to them.
    public let sessions: [String]

    public init(job: String, status: String, text: String, sessions: [String] = []) {
        self.job = job
        self.status = status
        self.text = text
        self.sessions = sessions
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(job: try c.decode(String.self, forKey: .job), status: try c.decode(String.self, forKey: .status),
                  text: try c.decode(String.self, forKey: .text),
                  sessions: try c.decodeIfPresent([String].self, forKey: .sessions) ?? [])
    }

    private enum CodingKeys: String, CodingKey { case job, status, text, sessions }
}

/// An update card (#286): the finished work of one or more sessions, one markdown row each, as
/// the core's `reply.card` event sends it. A card that gains a row comes again whole, with the
/// same `id`.
public struct ReplyCard: Decodable, Sendable, Equatable {
    public let id: UInt64
    public let text: String
    public let count: Int
    public let lines: [UInt64]
    /// The sessions the rows are about: the card's buttons that go to them.
    public let sessions: [String]

    public init(id: UInt64, text: String, count: Int, lines: [UInt64], sessions: [String] = []) {
        self.id = id
        self.text = text
        self.count = count
        self.lines = lines
        self.sessions = sessions
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(id: try c.decode(UInt64.self, forKey: .id), text: try c.decode(String.self, forKey: .text),
                  count: try c.decode(Int.self, forKey: .count), lines: try c.decode([UInt64].self, forKey: .lines),
                  sessions: try c.decodeIfPresent([String].self, forKey: .sessions) ?? [])
    }

    private enum CodingKeys: String, CodingKey { case id, text, count, lines, sessions }
}

/// The text box under Pixie (#221, spec docs/superpowers/specs/2026-10-21-text-mode-design.md
/// §4.3): one message at a time, and only the coordinator's spoken answer to it, typed out word
/// by word. The controller feeds it events and a timer (`tick`); the view draws it. It also
/// carries captions (#224): what Pixie says aloud, shown in the same place (voice mode: without
/// the input box).
public struct QuickChatModel: Equatable, Sendable {
    public enum Phase: Equatable, Sendable { case ready, thinking, answering, done, stopped }

    public private(set) var phase: Phase = .ready
    /// The answer as far as it is shown; `full` is all of it.
    public private(set) var shown = ""
    public private(set) var full = ""
    /// The typed job the box is showing (nil before the first send).
    public private(set) var job: String?
    /// The box is open (text mode only); the controller sets it. Open, it shows any late answer
    /// itself, so that answer no longer shows on its own once it closes (#259).
    public var open = false {
        didSet { if open { lateShown = false } }
    }
    /// replyMode: "voice" or "text".
    public var mode = "voice"

    /// The newest typed message's job, kept when the ✕ or a caption puts it away: a late answer
    /// to it (a session's reply, #241) still shows.
    private var lastJob: String?
    /// The sessions what is shown is about (#261): the card's buttons that go to them.
    public private(set) var sessions: [String] = []
    /// The update card shown (#286), nil for anything else, and how many rows it holds.
    public private(set) var cardID: UInt64?
    public private(set) var count = 1
    /// The sessions Pixie was just told of: its next reply is about them (#261).
    private var aboutNext: [String] = []

    /// The answer to the last message came while the box was closed (#259): it shows on its own,
    /// as a caption would, until it is put away or the next message goes.
    private var lateShown = false
    /// Lines that came for a job before chat.send said which job is ours (newest per job).
    private var early: [String: ChatLine] = [:]
    private var pending = false
    /// Stop came while pending: the job is cancelled as soon as it is known.
    private var stopPending = false
    /// What is shown is a caption (Pixie's spoken words), not a typed message's answer.
    public private(set) var isCaption = false
    /// The caption is your own words (#251), not Pixie's.
    public private(set) var isYou = false
    /// The bubble above the caption (#254): the other side's last words, until they fade.
    public struct Earlier: Equatable, Sendable {
        public let text: String
        public let isYou: Bool
        /// When it moved up: it fades `fadeSecs` later.
        public let at: Date
        public init(text: String, isYou: Bool, at: Date) {
            self.text = text
            self.isYou = isYou
            self.at = at
        }
    }

    public enum EarlierPhase: Equatable, Sendable { case shown, fading, gone }

    public private(set) var earlier: Earlier?
    /// captionFadeSecs: how long the earlier bubble stays before it fades (0: only the latest).
    public var fadeSecs = 4
    /// How long the fade itself takes.
    public static let fadeDuration: TimeInterval = 0.5

    /// Shown, fading out, or gone by `now`.
    public func earlierPhase(now: Date) -> EarlierPhase {
        guard let earlier, fadeSecs > 0 else { return .gone }
        let age = now.timeIntervalSince(earlier.at)
        if age < TimeInterval(fadeSecs) { return .shown }
        return age < TimeInterval(fadeSecs) + Self.fadeDuration ? .fading : .gone
    }

    /// The other side starts speaking: the caption up to now moves up (#254).
    private mutating func moveUp(beforeYou you: Bool, now: Date) {
        guard isCaption, captionLive, isYou != you, !full.isEmpty else { return }
        earlier = fadeSecs > 0 ? Earlier(text: full, isYou: isYou, at: now) : nil
    }

    /// Pixie's words came as it said them (#253): its final transcript completes them, or, once
    /// you talk again, leaves your words be.
    private var awaitsFinal = false
    /// The caption's conversation is still going (it shows on its own until it ends).
    private var captionLive = false
    /// When what is shown was said: the card's "2m ago" (#224).
    public private(set) var saidAt: Date?

    public init() {}

    // MARK: Pixie's earlier replies (#263)

    /// One of Pixie's replies, kept to step back to.
    public struct Reply: Equatable, Sendable {
        public let text: String
        public let sessions: [String]
        public let at: Date
        /// The update card it was (#286), and how many rows it holds.
        public let id: UInt64?
        public let count: Int
        public init(text: String, sessions: [String], at: Date, id: UInt64? = nil, count: Int = 1) {
            self.text = text
            self.sessions = sessions
            self.at = at
            self.id = id
            self.count = count
        }
    }

    /// Where the card is among the replies: `index` of `count`, the newest last.
    public struct Position: Equatable, Sendable {
        public let index: Int
        public let count: Int
        public init(index: Int, count: Int) {
            self.index = index
            self.count = count
        }
    }

    /// replyHistoryMins in seconds: replies older than this drop off (0 keeps only the latest).
    public var historySecs = 300
    /// Pixie's replies before the one shown, oldest first.
    public private(set) var history: [Reply] = []
    /// The reply you stepped back to (an index into `history`); nil shows the newest.
    private var viewing: Int?
    /// A new reply came while you looked back: » goes to it.
    public private(set) var newerWaiting = false

    public var viewed: Reply? { viewing.map { history[$0] } }
    /// Words are up (or on their way); not once they're put away.
    private var hasCurrent: Bool { phase != .ready }
    /// Nothing current, but replies kept to step back to: the card says "Earlier replies" with ‹.
    public var showsEarlierOnly: Bool { !hasCurrent && viewing == nil && !history.isEmpty }
    /// Nil with nothing to step back to (no arrows), or with nothing current and not looking back.
    public var position: Position? {
        guard !history.isEmpty else { return nil }
        guard hasCurrent else { return viewing.map { Position(index: $0 + 1, count: history.count) } }
        return Position(index: (viewing ?? history.count) + 1, count: history.count + 1)
    }
    /// Older cards peeking behind the one shown (#286): up to two.
    public var peeks: Int { position.map { min(2, max(0, $0.index - 1)) } ?? 0 }
    /// How many updates the card shown holds (#286).
    public var shownCount: Int { viewed?.count ?? (cardID != nil ? count : 1) }
    /// The card's header (#286): "Pixie · 3 updates · 2m ago"; the count only when combined.
    public static func header(who: String, count: Int, ago: String?) -> String {
        ([who] + (count > 1 ? ["\(count) updates"] : []) + (ago.map { [$0] } ?? [])).joined(separator: " · ")
    }
    public var canBack: Bool { !history.isEmpty && viewing != 0 }
    public var canForward: Bool { viewing != nil }
    /// What the card shows: the reply you stepped back to, else the newest.
    public var shownText: String { viewed?.text ?? shown }
    /// All of what is shown, typed out or not: its links and files are found in this (#277).
    public var shownFull: String { viewed?.text ?? full }
    public var shownSessions: [String] { viewed?.sessions ?? sessions }

    public mutating func back(now: Date) {
        prune(now: now)
        guard canBack else { return }
        viewing = (viewing ?? history.count) - 1
    }

    public mutating func forward() {
        guard let viewing else { return }
        if viewing + 1 < history.count {
            self.viewing = viewing + 1
        } else {
            toNewest()
        }
    }

    /// » (or the card going away, or your own words): the newest again.
    public mutating func toNewest() {
        viewing = nil
        newerWaiting = false
    }

    /// Drops replies older than `historySecs`, the one you look at too. True when any went.
    @discardableResult
    public mutating func prune(now: Date) -> Bool {
        let keep = history.drop { now.timeIntervalSince($0.at) > TimeInterval(historySecs) || historySecs == 0 }
        let gone = history.count - keep.count
        guard gone > 0 else { return false }
        history = Array(keep)
        if let v = viewing {
            if v - gone >= 0 {
                viewing = v - gone
            } else {
                toNewest()
            }
        }
        return true
    }

    private mutating func clearCard() {
        cardID = nil
        count = 1
    }

    /// A new reply replaces what is shown: Pixie's words go into the history (not yours, nor a
    /// line about work still running). Looking back, you stay there and » offers the new one.
    private mutating func keep(now: Date) {
        // A typed message's answer or a caption: not why a message couldn't go (no job).
        if !isYou, !full.isEmpty, let saidAt, phase == .done || phase == .answering, isCaption || job != nil {
            history.append(Reply(text: full, sessions: sessions, at: saidAt, id: cardID, count: count))
        }
        if viewing != nil { newerWaiting = true }
        prune(now: now)
    }

    public var showsStop: Bool { phase == .thinking || (phase == .answering && !isCaption) }

    /// A live caption is up (voice mode draws the card for it alone).
    public var showsCaption: Bool { isCaption && captionLive && phase != .ready }

    /// There is something said to show again: a caption, or a typed message's finished answer.
    private var hasLast: Bool {
        saidAt != nil && !full.isEmpty && (phase == .done || (isCaption && phase == .answering))
    }

    /// The card without the input line: a live caption, or the last words on hover or always.
    public func showsCard(hovering: Bool, always: Bool) -> Bool {
        (hovering && !history.isEmpty) || showsCaption || (lateShown && !isCaption && !full.isEmpty && (phase == .answering || phase == .done)) || (lateShown && cardID != nil && phase == .done) || ((hovering || always) && hasLast)
    }

    /// How long ago it was said ("just now", "2m ago"), once it is all shown.
    public func ago(now: Date) -> String? {
        if let viewed { return Self.ago(viewed.at, now: now) }
        guard let saidAt, phase == .done else { return nil }
        return Self.ago(saidAt, now: now)
    }

    private static func ago(_ at: Date, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(at)))
        switch s {
        case ..<60: return "just now"
        case ..<3600: return "\(s / 60)m ago"
        case ..<86400: return "\(s / 3600)h ago"
        default: return "\(s / 86400)d ago"
        }
    }

    public var statusText: String? {
        if isCaption { return nil }
        switch phase {
        case .thinking: return "Thinking…"
        case .answering: return "Answering…"
        default: return nil
        }
    }

    /// Words or attachments (#240) to send, and nothing at work.
    public func canSend(_ draft: String, attachments: Int = 0) -> Bool {
        (attachments > 0 || !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) && !showsStop
    }

    /// The message is on its way (before chat.send answers): thinking, and lines that come
    /// meanwhile are kept until the job is known.
    public mutating func sending(now: Date = Date()) {
        keep(now: now)
        clearCard()
        toNewest()
        isCaption = false
        isYou = false
        awaitsFinal = false
        earlier = nil
        captionLive = false
        saidAt = nil
        job = nil
        lastJob = nil
        lateShown = false
        sessions = []
        pending = true
        stopPending = false
        early = [:]
        phase = .thinking
        shown = ""
        full = ""
    }

    /// The message went: its job's lines are what the box shows now (any that came early too).
    /// True when Stop came first, so the core must cancel this job now.
    @discardableResult
    public mutating func sent(job: String) -> Bool {
        clearCard()
        self.job = job
        lastJob = job
        lateShown = false
        sessions = []
        isCaption = false
        isYou = false
        awaitsFinal = false
        earlier = nil
        captionLive = false
        if stopPending {
            stopPending = false
            pending = false
            early = [:]
            return true
        }
        let kept = pending ? early[job] : nil
        pending = false
        early = [:]
        phase = .thinking
        shown = ""
        full = ""
        if let kept { apply(kept) }
        return false
    }

    /// The message couldn't go: the reason is the answer.
    public mutating func failedToSend(_ reason: String) {
        clearCard()
        lateShown = false
        sessions = []
        isCaption = false
        isYou = false
        awaitsFinal = false
        earlier = nil
        captionLive = false
        saidAt = Date()
        job = nil
        pending = false
        stopPending = false
        early = [:]
        full = reason
        shown = reason
        phase = .done
    }

    public mutating func apply(_ line: ChatLine, now: Date = Date()) {
        if pending {
            early[line.job] = line
            return
        }
        if line.job == lastJob, line.status != "working",
           isCaption || line.job != job || phase == .ready {
            // #241: a late answer to the last message brings the card back with it (not after
            // Stop: the cancel's own line doesn't replace "Stopped.").
            keep(now: now)
            clearCard()
            job = line.job
            isCaption = false
            isYou = false
            awaitsFinal = false
            earlier = nil
            captionLive = false
            full = ""
            shown = ""
            phase = .done
        }
        guard !isCaption, line.job == job, phase == .thinking || phase == .answering || phase == .done else { return }
        sessions = line.sessions
        if line.status == "working" {
            full = line.text
            shown = line.text
            phase = .thinking
        } else if !line.text.isEmpty && line.text == full && shown == full {
            // Only the end of what it already shows (a hand-off, #231): no typing it out again.
            phase = .done
            saidAt = now
        } else if line.text == full && phase == .answering {
            saidAt = now // the same words while they type out: the same reply
        } else {
            keep(now: now)
            clearCard()
            full = line.text
            shown = ""
            phase = .answering
            saidAt = now
            if !open {
                lateShown = true // new words with the box closed: nothing else says them (#259)
            }
        }
    }

    /// The words can be put away (the ✕): a caption, or a finished answer; never while a typed
    /// message is at work (Stop is there then).
    public var canDismiss: Bool {
        isCaption ? phase != .ready : (phase == .done || phase == .stopped)
    }

    /// The ✕ (#224): the words go; hover and always show nothing until something new is said.
    public mutating func dismiss(now: Date = Date()) {
        guard canDismiss else { return }
        keep(now: now)
        clearCard()
        toNewest()
        lateShown = false
        sessions = []
        isCaption = false
        isYou = false
        awaitsFinal = false
        earlier = nil
        captionLive = false
        saidAt = nil
        full = ""
        shown = ""
        phase = .ready
    }

    /// Puts the words away `secs` after they were said (0 keeps them), never while the
    /// conversation they came from goes on. True when it did.
    public mutating func dismissIfOlder(than secs: Int, now: Date) -> Bool {
        guard secs > 0, let saidAt, !(isCaption && captionLive), canDismiss, viewing == nil,
              now.timeIntervalSince(saidAt) >= TimeInterval(secs) else { return false }
        dismiss(now: now)
        return true
    }

    /// What Pixie said aloud (the voice model's transcript), typed out. Only Pixie's side: your
    /// words show as they're heard (`heard`); your final transcript can come after the reply. A
    /// new caption replaces the last. In text mode it never covers a typed message still at work.
    /// True when it shows.
    @discardableResult
    public mutating func caption(role: String, text: String, now: Date = Date()) -> Bool {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard role == "assistant", !words.isEmpty else { return false }
        if awaitsFinal {
            // Already shown as it was said (#253): complete it, unless you have talked since.
            awaitsFinal = false
            guard isCaption, !isYou else { return false }
            full = words
            shown = words
            phase = .done
            return true
        }
        guard takeCaption() else { return false }
        keep(now: now)
        clearCard()
        newReply()
        moveUp(beforeYou: false, now: now)
        isCaption = true
        isYou = false
        captionLive = true
        saidAt = now
        job = nil
        full = words
        shown = ""
        phase = .answering
        return true
    }

    /// Your words so far while you talk (#251): they replace the caption at once, with nothing to
    /// type out, until Pixie answers. True when they show.
    @discardableResult
    public mutating func heard(_ text: String, now: Date = Date()) -> Bool {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !words.isEmpty, takeCaption() else { return false }
        keep(now: now)
        clearCard()
        toNewest()
        moveUp(beforeYou: true, now: now)
        sessions = []
        isCaption = true
        isYou = true
        captionLive = true
        saidAt = now
        job = nil
        full = words
        shown = words
        phase = .done
        return true
    }

    /// Pixie's words so far while it speaks (#253): they replace the caption at once, with
    /// nothing to type out; its final transcript completes them. True when they show.
    @discardableResult
    public mutating func saying(_ text: String, now: Date = Date()) -> Bool {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !words.isEmpty, takeCaption() else { return false }
        if !awaitsFinal || isYou || !isCaption {
            keep(now: now)
            clearCard()
            newReply()
        }
        moveUp(beforeYou: false, now: now)
        isCaption = true
        isYou = false
        awaitsFinal = true
        captionLive = true
        saidAt = now
        job = nil
        full = words
        shown = words
        phase = .done
        return true
    }

    /// An update card (#286): the finished work of sessions, shown on its own like a caption but
    /// never over a typed message at work (it goes into the history, » offers it). The card
    /// already up comes again with more rows: it is replaced in place. True when it shows.
    @discardableResult
    public mutating func update(_ card: ReplyCard, now: Date = Date()) -> Bool {
        if cardID == card.id, hasCurrent {
            popUp(card, now: now)
            return true
        }
        if let i = history.firstIndex(where: { $0.id == card.id }) {
            if hasCurrent || i != history.count - 1 || !takeCaption() {
                // The card was pushed back by a newer one: its entry gains the row, no second card.
                history[i] = Reply(text: card.text, sessions: card.sessions, at: history[i].at, id: card.id, count: card.count)
                return false
            }
            // Put away and gaining a row: it comes back as the shown card.
            history.remove(at: i)
            if viewing != nil { toNewest() }
            keep(now: now)
            showCard(card, now: now)
            return true
        }
        guard takeCaption() else {
            history.append(Reply(text: card.text, sessions: card.sessions, at: now, id: card.id, count: card.count))
            newerWaiting = true
            prune(now: now)
            return false
        }
        keep(now: now)
        showCard(card, now: now)
        return true
    }

    /// The shown card gained a row: replaced in place, and up again for the dismiss time.
    private mutating func popUp(_ card: ReplyCard, now: Date) {
        full = card.text
        shown = card.text
        count = card.count
        sessions = card.sessions
        saidAt = now
        lateShown = true
    }

    private mutating func showCard(_ card: ReplyCard, now: Date) {
        isCaption = true
        isYou = false
        captionLive = false
        awaitsFinal = false
        earlier = nil
        cardID = card.id
        job = nil
        phase = .done
        popUp(card, now: now)
    }

    /// Pixie was told of these sessions (#261): its next reply, when it starts, is about them.
    public mutating func about(_ names: [String]) {
        aboutNext = names
    }

    /// Pixie starts a new reply: it is about the sessions it was just told of (#261).
    private mutating func newReply() {
        sessions = aboutNext
        aboutNext = []
    }

    /// A caption may show: in text mode, not over a typed message still at work.
    private func takeCaption() -> Bool {
        !(mode == "text" && !isCaption && (pending || phase == .thinking || phase == .answering))
    }

    /// The voice conversation ended: the caption stops showing on its own, all of it shown, and
    /// is kept for hover or always-show (#224).
    public mutating func conversationEnded() {
        awaitsFinal = false // a streamed reply cut short gets no final words (#253)
        aboutNext = []
        guard isCaption else { return }
        captionLive = false
        if phase == .answering {
            shown = full
            phase = .done
        }
    }

    /// Shows the next word of the answer; false when nothing changed. The last word ends it.
    public mutating func tick() -> Bool {
        guard phase == .answering else { return false }
        let words = full.split(separator: " ", omittingEmptySubsequences: true)
        let have = shown.split(separator: " ", omittingEmptySubsequences: true).count
        guard have < words.count else {
            phase = .done
            return false
        }
        shown = words[0...have].joined(separator: " ")
        if have + 1 == words.count {
            shown = full
            phase = .done
        }
        return true
    }

    /// Stop: while thinking the work stops ("Stopped."); while typing out, the rest shows at once.
    /// True when the core must cancel the job (only while thinking: nothing runs while typing out).
    @discardableResult
    public mutating func stopped() -> Bool {
        switch phase {
        case .thinking:
            phase = .stopped
            if pending {
                // No job yet: sent(job:) says to cancel it.
                stopPending = true
                return false
            }
            return true
        case .answering:
            shown = full
            phase = .done
        default: break
        }
        return false
    }
}
