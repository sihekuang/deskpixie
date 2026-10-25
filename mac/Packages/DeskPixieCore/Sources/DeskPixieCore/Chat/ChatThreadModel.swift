import Foundation
import Observation
import os

/// The chat window's thread (#302): the entries shown oldest first, the requests waiting on
/// you, and the box's reply target. The controller feeds it the core's `chat.entry`,
/// `chat.needs`, `chat.badge`, `chat.reset` and `chat.live` events; the views only read it and call its
/// methods. Calls run off the main actor.
@MainActor
@Observable
public final class ChatThreadModel {
    /// How many entries one page holds (the core's `chat.PageSize`).
    nonisolated public static let pageSize = 50

    /// The thread, oldest first, for drawing.
    public private(set) var entries: [ChatEntry] = []
    /// The requests waiting on you, oldest first.
    public private(set) var needs: [ChatEntry] = []
    /// Entries you have not read, as the core counts them (not summed from `entries`).
    public private(set) var unread = 0
    /// The last badge preview that came, for Pixie's badge.
    public private(set) var preview: ChatBadge.Preview?
    /// Who the box replies to; nil = Pixie.
    public var replyTo: ChatWho?
    /// The question the box answers (its token), when Reply… was on a card that takes a typed
    /// answer: the message goes as `agent.typedAnswer` while the question is still open.
    public private(set) var replyToken: String?
    /// A message is on its way to the core.
    public private(set) var sending = false
    /// A typed message is being worked on; the coordinator's or Pixie's answer clears it.
    public private(set) var thinking = false
    /// Words still coming, never stored: yours and Pixie's while you talk, and each typed job's
    /// step while the coordinator works on it. The last one changed is last.
    public private(set) var live: [ChatLive] = []
    /// Older entries remain to load.
    public private(set) var hasOlder = false
    /// Why the last call failed; cleared by the next one that works.
    public private(set) var error: String?
    /// Who a message can go to on its own (`chat.recipients`), for the @ list and the rule
    /// that a message has one recipient.
    public private(set) var recipients: [ChatRecipient] = []
    /// The list has come at least once.
    public private(set) var recipientsKnown = false
    /// The core refused the last send, in its words (one recipient per message); the draft
    /// stays. Any other failure is `error`. Editing the text or the target clears it.
    public private(set) var refusal: String?

    @ObservationIgnored private let client: any ChatClient
    @ObservationIgnored private var loads = 0
    @ObservationIgnored private var readUpTo: String?
    @ObservationIgnored private var loading = false
    @ObservationIgnored private var replay: [ChatEntry] = []
    /// How many `chat.recipients` events have come: a call's answer asked before the last one
    /// is older than it.
    @ObservationIgnored private var recipientEvents = 0
    @ObservationIgnored private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "chat")

    public init(client: any ChatClient) {
        self.client = client
    }

    // MARK: Reading

    /// Shows the last page, the open requests and the badge counts. A newer load drops an older
    /// one's result; events that came while it ran are applied over the page.
    public func load() async {
        Task { await loadRecipients() }
        loads += 1
        let mine = loads
        loading = true
        replay = []
        let client = client
        let result = await Task.detached { () -> Result<([ChatEntry], [ChatEntry], ChatBadge), Error> in
            Result { (try client.entries(before: nil, limit: Self.pageSize), try client.needs(), try client.badge()) }
        }.value
        guard mine == loads else { return }
        loading = false
        switch result {
        case .success(let (page, open, badge)):
            entries = Self.unique(page.reversed())
            needs = open
            unread = badge.unread
            hasOlder = page.count >= Self.pageSize
            readUpTo = nil
            error = nil
            let missed = replay
            for e in missed { apply(e) }
        case .failure(let err):
            fail("chat.entries", err)
        }
        replay = []
    }

    /// Asks the core who can be messaged now; the `chat.recipients` event keeps it current. An
    /// event that came while it was asked is newer, so the answer is dropped.
    public func loadRecipients() async {
        let asked = recipientEvents
        let client = client
        let result = await Task.detached { Result { try client.recipients() } }.value
        guard asked == recipientEvents else { return }
        if case .success(let rs) = result { setRecipients(rs) }
    }

    /// `chat.recipients`: the whole list now.
    public func applyRecipients(_ rs: [ChatRecipient]) {
        recipientEvents += 1
        setRecipients(rs)
    }

    private func setRecipients(_ rs: [ChatRecipient]) {
        recipients = rs
        recipientsKnown = true
    }

    /// Puts the page before the oldest entry shown in front of it.
    public func loadOlder() async {
        guard hasOlder, let first = entries.first?.id else { return }
        let mine = loads
        let client = client
        let result = await Task.detached {
            Result { try client.entries(before: first, limit: Self.pageSize) }
        }.value
        guard mine == loads, entries.first?.id == first else { return }
        switch result {
        case .success(let page):
            let shown = Set(entries.map(\.id))
            entries = Self.unique(page.reversed()).filter { !shown.contains($0.id) } + entries
            hasOlder = page.count >= Self.pageSize
            error = nil
        case .failure(let err):
            fail("chat.entries", err)
        }
    }

    private static func unique(_ es: [ChatEntry]) -> [ChatEntry] {
        var seen = Set<String>()
        return es.filter { seen.insert($0.id).inserted }
    }

    // MARK: Events

    /// `chat.entry`: an entry with an ID already shown replaces it in place; a new one goes
    /// last, unless it is older than what is shown: then the page of older entries carries it
    /// (`hasOlder`) or it goes in by time. Pixie's or the coordinator's new entry ends the wait
    /// for an answer.
    public func apply(_ e: ChatEntry) {
        if loading { replay.append(e) }
        if let i = needs.firstIndex(where: { $0.id == e.id }) {
            if e.request?.outcome.isEmpty == false { needs.remove(at: i) } else { needs[i] = e }
        }
        if let i = entries.firstIndex(where: { $0.id == e.id }) {
            if entries[i].text != e.text { clearLive(for: e) }
            entries[i] = e
            return
        }
        clearLive(for: e)
        if let first = entries.first, e.at < first.at, hasOlder { return }
        entries.insert(e, at: entries.firstIndex { $0.at > e.at } ?? entries.count)
        if e.who.kind == "pixie" || e.who.kind == "coordinator" { thinking = false }
    }

    /// `chat.live`: who's line (the job's, for the coordinator) is now this text; empty clears it.
    public func applyLive(_ l: ChatLive) {
        live.removeAll { $0.id == l.id }
        if !l.text.isEmpty { live.append(l) }
    }

    /// An entry's words are final: your or Pixie's live line goes, in case its clear crossed
    /// with them. The coordinator's steps are left to the core, which clears each job's before
    /// its entry (an entry's ID is opaque, so it can't be matched to a job here).
    private func clearLive(for e: ChatEntry) {
        guard e.who.kind == "you" || e.who.kind == "pixie" else { return }
        live.removeAll { $0.who == e.who.kind }
    }

    /// `chat.reset`: every live line goes. Called as the event comes, before `reset()` runs, so
    /// a `chat.live` that follows the reset isn't wiped by it.
    public func clearLive() {
        live = []
    }

    /// Your words or Pixie's still coming ("you" or "pixie"), nil when none.
    public func liveText(_ who: String) -> String? {
        live.last { $0.who == who }?.text
    }

    /// The coordinator's latest step, nil when it has none.
    public var coordinatorStep: String? { liveText("coordinator") }

    /// "Thinking…" shows while a typed message works, or while the coordinator has a step.
    public var showsThinking: Bool { thinking || coordinatorStep != nil }

    /// "Thinking…", or "Thinking… · <step>".
    public var thinkingLabel: String {
        guard let step = coordinatorStep else { return "Thinking…" }
        return "Thinking… · \(step)"
    }

    /// `chat.needs`: the open requests now.
    public func applyNeeds(_ n: [ChatEntry]) {
        needs = n
    }

    /// `chat.badge`: the counts to show. The unread count is the core's.
    public func applyBadge(_ b: ChatBadge) {
        unread = b.unread
        if let p = b.preview { preview = p }
    }

    /// `chat.reset`: entries went; start again from the end.
    public func reset() async {
        entries = []
        needs = []
        hasOlder = false
        await load()
    }

    // MARK: Acting

    /// The window shows the newest entry: tells the core everything up to it is read.
    public func sawBottom() {
        guard let newest = entries.last?.id, newest != readUpTo else { return }
        readUpTo = newest
        let client = client
        Task { @MainActor [weak self] in
            let result = await Task.detached { Result { try client.read(upTo: newest) } }.value
            if case .failure(let err) = result, let self {
                if self.readUpTo == newest { self.readUpTo = nil }
                self.fail("chat.read", err)
            }
        }
    }

    /// Sends the box's text to `replyTo`'s session, or to Pixie. The reply target clears; a
    /// failed send puts it back and keeps the entries.
    /// Replying to a question that takes a typed answer sends it as that answer
    /// (`agent.typedAnswer`) while the question is open; otherwise the message goes to its session.
    /// One at a time: while a send is on its way another is refused (a second Return would send
    /// the same attachments again). True when it went.
    @discardableResult
    public func send(_ text: String, attachments: [String] = []) async -> Bool {
        guard !sending else { return false }
        sending = true
        defer { sending = false }
        let target = replyTo
        let token = target == nil ? nil : replyToken
        replyTo = nil
        replyToken = nil
        if target == nil { thinking = true }
        let client = client
        let session = target?.name
        let answer = token.flatMap { t in needs.contains { $0.request?.token == t } ? t : nil }
        let result = await Task.detached {
            Result {
                if let answer {
                    try client.typedAnswer(token: answer, text: text)
                } else {
                    try client.send(text, attachments: attachments, session: session)
                }
            }
        }.value
        switch result {
        case .success:
            error = nil
            refusal = nil
            return true
        case .failure(let err):
            if replyTo == nil {
                replyTo = target
                replyToken = token
            }
            thinking = false
            if answer == nil, case DeskPixieEngineError.refused(let why) = err {
                // The core said why, in words for you (one recipient per message).
                refusal = why
                error = nil
                Self.log.error("chat.send: \(why, privacy: .public)")
            } else {
                fail(answer == nil ? "chat.send" : "agent.typedAnswer", err)
            }
            return false
        }
    }

    /// What stops this text from going as it is, said under the box: the one-recipient rule
    /// for the current target, checked here before the core does. Nil: it can go. A typed
    /// answer to an open question isn't a `chat.send`, and the core doesn't apply the rule to
    /// it, so neither does the box.
    public func check(_ text: String) -> String? {
        if let t = replyToken, needs.contains(where: { $0.request?.token == t }) { return nil }
        return ChatRecipients.check(text, target: replyTo?.name,
                                    label: replyTo.map(Self.name(of:)) ?? "",
                                    recipients: recipients, knowsRecipients: recipientsKnown)
    }

    /// The line under the box: the core's refusal, else this text's own problem.
    public func notice(for text: String) -> String? { refusal ?? check(text) }

    /// The text changed: the core's last refusal no longer holds.
    public func draftChanged() { refusal = nil }

    /// A pick from the @ list: this recipient is now the one target, or (nil) Pixie.
    public func choose(_ r: ChatRecipient?) {
        replyTo = r?.who
        replyToken = nil
        refusal = nil
    }

    /// Send is offered: words or attachments to send, no send already on its way, and nothing
    /// the one-recipient rule refuses.
    public func canSend(_ text: String, attachments: Int) -> Bool {
        !sending && (!text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || attachments > 0)
            && check(text) == nil
    }

    public func decide(token: String, allow: Bool) async {
        let client = client
        await call("agent.decide") { try client.decide(token: token, allow: allow) }
    }

    /// `option` counts from 1.
    public func choose(token: String, option: Int) async {
        let client = client
        await call("agent.choose") { try client.choose(token: token, option: option) }
    }

    public func typedAnswer(token: String, text: String) async {
        let client = client
        await call("agent.typedAnswer") { try client.typedAnswer(token: token, text: text) }
    }

    public func stop() async {
        let client = client
        thinking = false
        await call("chat.stop") { try client.stop() }
    }

    /// Empties the thread. The core's `chat.reset` that follows reloads it.
    public func clear() async {
        let client = client
        await call("chat.clear") { try client.clear() }
    }

    /// Aims the box at the session at this address: its label comes from the newest entry that
    /// has the address, else the address itself.
    public func reply(toSession address: String) {
        replyTo = who(forSession: address)
        replyToken = nil
        refusal = nil
    }

    /// Reply… on a card or a session's message: a question that takes a typed answer gets it;
    /// any other goes to its session as a message. The label is the session's newest, else the
    /// entry's own.
    public func reply(to entry: ChatEntry) {
        let shown = (entries + needs).contains { $0.who.name == entry.who.name && !$0.who.label.isEmpty }
        replyTo = shown || entry.who.label.isEmpty ? who(forSession: entry.who.name) : entry.who
        replyToken = nil
        refusal = nil
        if let r = entry.request, Self.isOpen(entry), r.typed, !r.token.isEmpty { replyToken = r.token }
    }

    /// × or Esc on the chip: back to messaging Pixie.
    public func cancelReply() {
        replyTo = nil
        replyToken = nil
        refusal = nil
    }

    /// The session at this address as its newest entry names it, else by the address itself.
    public func who(forSession address: String) -> ChatWho {
        let newest = (entries + needs).last { $0.who.name == address && !$0.who.label.isEmpty }
        return newest?.who ?? ChatWho(kind: "session", name: address, label: address)
    }

    /// Where the window opens: the oldest entry you haven't read, else the newest. The core's
    /// unread count (from the badge `load()` fetched) decides how many: the oldest of the last
    /// that many entries not marked read and not yours (your own words never are).
    public var openTarget: String? {
        let notRead = entries.filter { !$0.read && $0.who.kind != "you" }
        if unread > 0, !notRead.isEmpty { return notRead[max(0, notRead.count - unread)].id }
        return entries.last?.id
    }

    // MARK: Cards

    /// A request still waiting on you.
    public static func isOpen(_ e: ChatEntry) -> Bool {
        e.request.map { $0.outcome.isEmpty } ?? false
    }

    /// Allow and Deny: a permission the core can answer.
    public static func offersDecision(_ e: ChatEntry) -> Bool {
        isOpen(e) && e.request?.kind == "permission" && e.request?.token.isEmpty == false
    }

    /// One button per option (`agent.choose`).
    public static func offersOptions(_ e: ChatEntry) -> Bool {
        guard isOpen(e), let r = e.request else { return false }
        return r.kind == "question" && !r.token.isEmpty && !r.options.isEmpty
    }

    /// Reply…: a question that takes a typed answer, or one with no buttons, answered by a
    /// message to its session.
    public static func offersReply(_ e: ChatEntry) -> Bool {
        guard isOpen(e), let r = e.request, r.kind == "question" else { return false }
        if !r.token.isEmpty { return r.typed }
        return e.who.kind == "session" && !e.who.name.isEmpty
    }

    /// The terminal button by who said it: a session of yours opens its own tab; one of Pixie's
    /// (a worker) its pane, through its job when the entry has one, so a worker that ended
    /// falls back to the coordinator; the coordinator its pane. Pixie and you have none, nor
    /// does an entry with no address to go to.
    public static func terminalTarget(for e: ChatEntry) -> ChatTerminalTarget? {
        let label = "Open \(name(of: e.who))'s terminal"
        switch e.who.kind {
        case "coordinator":
            return ChatTerminalTarget(call: .focusAgent, name: "coordinator", label: label)
        case "session":
            guard !e.who.name.isEmpty else { return nil }
            if e.who.yours { return ChatTerminalTarget(call: .focusSession, name: e.who.name, label: label) }
            if let job = e.request?.job, !job.isEmpty {
                return ChatTerminalTarget(call: .focusJob, name: e.who.name, job: job, label: label)
            }
            return ChatTerminalTarget(call: .focusAgent, name: e.who.name, label: label)
        default:
            return nil
        }
    }

    /// A pill in the needs strip.
    /// "api-server wants to run git push origin fix-login" (the view cuts it with "…"); a
    /// permission with no text "wants permission".
    public static func pillLabel(_ e: ChatEntry) -> String {
        let who = name(of: e.who)
        guard e.request?.kind == "permission" else { return "\(who) asks a question" }
        let text = e.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if text.isEmpty { return "\(who) wants permission" }
        // Some requests already say what they want ("wants to edit …", from Codex).
        if text.hasPrefix("wants ") || text.hasPrefix("asks ") { return "\(who) \(text)" }
        return "\(who) wants to run \(text)"
    }

    /// Reply… under a session's own message (not a card): the box then messages that session.
    public static func offersSessionReply(_ e: ChatEntry) -> Bool {
        e.request == nil && e.who.kind == "session" && !e.who.name.isEmpty
    }

    /// The thread's end is in view: the bottom marker's lower edge, in the scroll view's own
    /// coordinates, is within a few points of the viewport's height (infinite = not laid out).
    /// The marker comes after the thread's bottom padding, so a scroll to it ends with the
    /// marker at the viewport's edge on every macOS.
    public static func isAtBottom(sentinelMaxY: Double, viewportHeight: Double, tolerance: Double = 8) -> Bool {
        viewportHeight > 0 && sentinelMaxY.isFinite && sentinelMaxY <= viewportHeight + tolerance
    }

    /// A scroll can move the content: it is taller than the viewport. The opening scroll then
    /// waits for the layout after it before anything counts as read; content that fits never
    /// moves, so its first layout already holds.
    public static func scrollMoves(contentHeight: Double, viewportHeight: Double) -> Bool {
        contentHeight > viewportHeight
    }

    /// The content changed height (rows grow once their links are found or their text wraps):
    /// a view that was at the bottom scrolls back to it. Scrolling alone never follows.
    public static func keepsBottom(wasAtBottom: Bool, heightChanged: Bool) -> Bool {
        wasAtBottom && heightChanged
    }

    /// The thread's viewport changed height (the box under it grew or shrank: a chip, the @
    /// list, more lines typed; or the window was resized): a view that was at the bottom before
    /// stays there. Decided by where it was, not by the end's position now: the new height may
    /// hide the end, and the content's height may be mid-flicker.
    public static func staysAtBottom(wasAtBottom: Bool, oldViewport: Double, newViewport: Double) -> Bool {
        wasAtBottom && oldViewport != newViewport
    }

    /// The newest entry counts as seen (`sawBottom`) only after the opening scroll, with the
    /// window on screen, and the end in view.
    public static func marksRead(opened: Bool, visible: Bool, atBottom: Bool) -> Bool {
        opened && visible && atBottom
    }

    /// Messages more than 5 minutes apart get a time divider; the first one always does.
    public static func startsNewTime(after previous: ChatEntry?, _ e: ChatEntry) -> Bool {
        guard let previous else { return true }
        return e.at - previous.at > 5 * 60 * 1000
    }

    // MARK: Copy

    public var placeholder: String {
        guard let w = replyTo else { return "Message Pixie…" }
        return "Reply to \(Self.name(of: w))…"
    }

    public static func needsLabel(_ n: Int) -> String {
        n == 1 ? "1 needs you" : "\(n) need you"
    }

    /// "<label> · your session|Pixie's agent · <status>" for a session; others show their label.
    public static func header(_ w: ChatWho) -> String {
        guard w.kind == "session" else { return name(of: w) }
        var parts = [name(of: w), w.yours ? "your session" : "Pixie's agent"]
        if !w.status.isEmpty { parts.append(w.status) }
        return parts.joined(separator: " · ")
    }

    /// What to call who said it: its label, else its address, else "Pixie", "Coordinator", "You".
    public static func displayName(_ w: ChatWho) -> String { name(of: w) }

    private static func name(of w: ChatWho) -> String {
        if !w.label.isEmpty { return w.label }
        if !w.name.isEmpty { return w.name }
        switch w.kind {
        case "pixie": return "Pixie"
        case "coordinator": return "Coordinator"
        case "you": return "You"
        default: return w.kind
        }
    }

    // MARK: Plumbing

    private func call(_ what: String, _ body: @escaping @Sendable () throws -> Void) async {
        let result = await Task.detached { Result(catching: body) }.value
        switch result {
        case .success: error = nil
        case .failure(let err): fail(what, err)
        }
    }

    private func fail(_ what: String, _ err: Error) {
        Self.log.error("\(what, privacy: .public): \(String(describing: err), privacy: .public)")
        error = "Couldn't reach Pixie. Try again."
    }
}
