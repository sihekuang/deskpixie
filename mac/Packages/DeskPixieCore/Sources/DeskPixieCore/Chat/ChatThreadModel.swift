import Foundation
import Observation
import os

/// The chat window's thread (#302): the entries shown oldest first, the requests waiting on
/// you, and the box's reply target. The controller feeds it the core's `chat.entry`,
/// `chat.needs`, `chat.badge` and `chat.reset` events; the views only read it and call its
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
    /// A typed message is being worked on; the coordinator's or Pixie's answer clears it.
    public private(set) var thinking = false
    /// Older entries remain to load.
    public private(set) var hasOlder = false
    /// Why the last call failed; cleared by the next one that works.
    public private(set) var error: String?

    @ObservationIgnored private let client: any ChatClient
    @ObservationIgnored private var loads = 0
    @ObservationIgnored private var readUpTo: String?
    @ObservationIgnored private var loading = false
    @ObservationIgnored private var replay: [ChatEntry] = []
    @ObservationIgnored private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "chat")

    public init(client: any ChatClient) {
        self.client = client
    }

    // MARK: Reading

    /// Shows the last page, the open requests and the badge counts. A newer load drops an older
    /// one's result; events that came while it ran are applied over the page.
    public func load() async {
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
            entries[i] = e
            return
        }
        if let first = entries.first, e.at < first.at, hasOlder { return }
        entries.insert(e, at: entries.firstIndex { $0.at > e.at } ?? entries.count)
        if e.who.kind == "pixie" || e.who.kind == "coordinator" { thinking = false }
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
    public func send(_ text: String, attachments: [String] = []) async {
        let target = replyTo
        replyTo = nil
        if target == nil { thinking = true }
        let client = client
        let session = target?.name
        let result = await Task.detached {
            Result { try client.send(text, attachments: attachments, session: session) }
        }.value
        switch result {
        case .success: error = nil
        case .failure(let err):
            if replyTo == nil { replyTo = target }
            thinking = false
            fail("chat.send", err)
        }
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
        let newest = (entries + needs).last { $0.who.name == address && !$0.who.label.isEmpty }
        replyTo = newest?.who ?? ChatWho(kind: "session", name: address, label: address)
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

    private static func name(of w: ChatWho) -> String {
        w.label.isEmpty ? (w.name.isEmpty ? w.kind : w.name) : w.label
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
