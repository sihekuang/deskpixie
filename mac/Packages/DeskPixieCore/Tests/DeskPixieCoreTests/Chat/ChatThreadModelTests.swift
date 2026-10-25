import XCTest
@testable import DeskPixieCore

/// A thread like the core's: newest first pages, an unknown `before` gives none, a refused
/// call throws.
final class FakeChatClient: ChatClient, @unchecked Sendable {
    struct Sent: Equatable { let text: String; let attachments: [String]; let session: String? }
    struct Failure: Error {}

    private let lock = NSLock()
    private var thread: [ChatEntry]
    private var open: [ChatEntry] = []
    private var _sent: [Sent] = []
    private var _reads: [String] = []
    private var failing = false
    private var _badge = ChatBadge()
    private var _clears = 0

    init(_ thread: [ChatEntry] = []) { self.thread = thread }

    var sent: [Sent] { lock.withLock { _sent } }
    var reads: [String] { lock.withLock { _reads } }
    var clears: Int { lock.withLock { _clears } }
    /// Runs inside `entries`, off the main actor, like an event arriving during a load.
    nonisolated(unsafe) var whileLoading: (@Sendable () -> Void)?
    func setBadge(_ b: ChatBadge) { lock.withLock { _badge = b } }
    func badge() throws -> ChatBadge { try check(); return lock.withLock { _badge } }
    func clear() throws { try check(); lock.withLock { _clears += 1; thread = []; open = [] } }
    func setFailing(_ f: Bool) { lock.withLock { failing = f } }
    func setNeeds(_ n: [ChatEntry]) { lock.withLock { open = n } }

    private func check() throws { if lock.withLock({ failing }) { throw Failure() } }

    func entries(before: String?, limit: Int) throws -> [ChatEntry] {
        try check()
        whileLoading?()
        return lock.withLock {
            var end = thread.count
            if let before {
                guard let i = thread.firstIndex(where: { $0.id == before }) else { return [] }
                end = i
            }
            return Array(thread[..<end].suffix(limit > 0 ? limit : ChatThreadModel.pageSize).reversed())
        }
    }
    func needs() throws -> [ChatEntry] { try check(); return lock.withLock { open } }
    func read(upTo: String) throws { try check(); lock.withLock { _reads.append(upTo) } }
    func send(_ text: String, attachments: [String], session: String?) throws {
        try check()
        lock.withLock { _sent.append(Sent(text: text, attachments: attachments, session: session)) }
    }
    func decide(token: String, allow: Bool) throws { try check() }
    func choose(token: String, option: Int) throws { try check() }
    func typedAnswer(token: String, text: String) throws { try check() }
    func stop() throws { try check() }
}

@MainActor
final class ChatThreadModelTests: XCTestCase {
    private func entry(_ n: Int, kind: String = "pixie", name: String = "", label: String = "") -> ChatEntry {
        ChatEntry(id: "e\(n)", at: Int64(n), who: ChatWho(kind: kind, name: name, label: label), text: "t\(n)")
    }

    nonisolated(unsafe) static let event7 = ChatEntry(id: "e7", at: 7, who: ChatWho(kind: "pixie"), text: "t7")

    private func waitFor(_ ok: @Sendable () -> Bool) async {
        for _ in 0..<200 where !ok() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    func testLoadShowsTheLastPageOldestFirst() async {
        let client = FakeChatClient((0..<60).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        XCTAssertEqual(model.entries.count, 50)
        XCTAssertEqual(model.entries.first?.id, "e10")
        XCTAssertEqual(model.entries.last?.id, "e59")
        XCTAssertTrue(model.hasOlder)
    }

    func testLoadOlderPrependsAndStopsAtTheStart() async {
        let client = FakeChatClient((0..<60).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        await model.loadOlder()
        XCTAssertEqual(model.entries.map(\.id), (0..<60).map { "e\($0)" })
        XCTAssertFalse(model.hasOlder)
        await model.loadOlder()
        XCTAssertEqual(model.entries.count, 60)
    }

    func testAnEntryWithAKnownIDReplacesInPlace() async {
        let model = ChatThreadModel(client: FakeChatClient((0..<3).map { entry($0) }))
        await model.load()
        var changed = entry(1)
        changed.text = "changed"
        model.apply(changed)
        XCTAssertEqual(model.entries.map(\.text), ["t0", "changed", "t2"])
        model.apply(entry(3))
        XCTAssertEqual(model.entries.map(\.id), ["e0", "e1", "e2", "e3"])
    }

    func testAnAnsweredRequestLeavesNeeds() {
        let model = ChatThreadModel(client: FakeChatClient())
        var ask = entry(1, kind: "session")
        ask.request = ChatRequest(token: "t", item: 1, kind: "question")
        model.applyNeeds([ask])
        XCTAssertEqual(model.needs.count, 1)
        ask.request?.outcome = "You answered: yes"
        model.apply(ask)
        XCTAssertTrue(model.needs.isEmpty)
    }

    func testSendingToAReplyTargetPassesItsSessionAndClearsIt() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        model.replyTo = ChatWho(kind: "session", name: "herdr:1.2", label: "api")
        await model.send("hi", attachments: ["/a.png"])
        XCTAssertEqual(client.sent, [.init(text: "hi", attachments: ["/a.png"], session: "herdr:1.2")])
        XCTAssertNil(model.replyTo)
        XCTAssertFalse(model.thinking, "a reply to a session is not Pixie working")
        await model.send("to pixie")
        XCTAssertNil(client.sent.last?.session)
    }

    func testPlaceholderAndNeedsLabelAndHeaderCopy() {
        let model = ChatThreadModel(client: FakeChatClient())
        XCTAssertEqual(model.placeholder, "Message Pixie…")
        model.replyTo = ChatWho(kind: "session", name: "a", label: "api")
        XCTAssertEqual(model.placeholder, "Reply to api…")
        XCTAssertEqual(ChatThreadModel.needsLabel(1), "1 needs you")
        XCTAssertEqual(ChatThreadModel.needsLabel(3), "3 need you")
        XCTAssertEqual(ChatThreadModel.header(ChatWho(kind: "session", name: "a", label: "api", yours: true, status: "working")),
                       "api · your session · working")
        XCTAssertEqual(ChatThreadModel.header(ChatWho(kind: "session", name: "a", label: "web")),
                       "web · Pixie's agent")
    }

    func testThinkingWhileATypedMessageWorks() async {
        let model = ChatThreadModel(client: FakeChatClient())
        await model.send("do it")
        XCTAssertTrue(model.thinking)
        model.apply(entry(1, kind: "session"))
        XCTAssertTrue(model.thinking, "a session's line is not the answer")
        model.apply(entry(2, kind: "coordinator"))
        XCTAssertFalse(model.thinking)
    }

    func testSawBottomReadsUpToTheNewest() async {
        let client = FakeChatClient((0..<3).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        model.sawBottom()
        await waitFor { client.reads == ["e2"] }
        model.sawBottom()
        model.apply(entry(3))
        model.sawBottom()
        await waitFor { client.reads == ["e2", "e3"] }
        XCTAssertEqual(client.reads, ["e2", "e3"], "the same bottom is told once")
    }

    func testUnreadComesFromTheBadge() {
        let model = ChatThreadModel(client: FakeChatClient())
        model.apply(entry(1))
        model.applyBadge(ChatBadge(needs: 1, unread: 4,
                                   preview: .init(id: "e1", who: ChatWho(kind: "pixie"), text: "t1")))
        XCTAssertEqual(model.unread, 4)
        XCTAssertEqual(model.preview?.id, "e1")
    }

    func testAFailedCallSetsAnErrorAndKeepsEntries() async {
        let client = FakeChatClient((0..<3).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        client.setFailing(true)
        await model.load()
        XCTAssertNotNil(model.error)
        XCTAssertEqual(model.entries.count, 3)
        model.replyTo = ChatWho(kind: "session", name: "a", label: "api")
        await model.send("x")
        XCTAssertEqual(model.replyTo?.name, "a", "a failed send keeps the target")
        XCTAssertFalse(model.thinking)
        client.setFailing(false)
        await model.load()
        XCTAssertNil(model.error)
    }

    func testResetReloadsFromTheStart() async {
        let client = FakeChatClient((0..<3).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        model.apply(entry(9))
        await model.reset()
        XCTAssertEqual(model.entries.map(\.id), ["e0", "e1", "e2"])
    }

    func testReplyChipTakesTheLabelFromTheNewestEntryWithThatAddress() async {
        let old = entry(1, kind: "session", name: "herdr:1.2", label: "old")
        let new = entry(2, kind: "session", name: "herdr:1.2", label: "api")
        let model = ChatThreadModel(client: FakeChatClient([old, new]))
        await model.load()
        model.reply(toSession: "herdr:1.2")
        XCTAssertEqual(model.replyTo?.label, "api")
        model.reply(toSession: "tmux:9")
        XCTAssertEqual(model.replyTo?.label, "tmux:9")
    }

    func testAMinimalEntryDecodes() throws {
        let json = #"{"id":"e0","at":5,"who":{"kind":"pixie","color":0},"text":"hi"}"#
        let e = try JSONDecoder().decode(ChatEntry.self, from: Data(json.utf8))
        XCTAssertEqual(e, ChatEntry(id: "e0", at: 5, who: ChatWho(kind: "pixie"), text: "hi"))
        let r = try JSONDecoder().decode(ChatRequest.self, from: Data(#"{"item":7,"kind":"permission"}"#.utf8))
        XCTAssertEqual(r.options, [])
        XCTAssertEqual(r.outcome, "")
        let b = try JSONDecoder().decode(ChatBadge.self, from: Data(#"{"needs":1,"unread":2}"#.utf8))
        XCTAssertNil(b.preview)
    }

    func testAFullEntryDecodes() throws {
        let json = #"""
        {"id":"req7","at":9,"who":{"kind":"session","name":"herdr:1.2","label":"api","yours":true,"status":"asking","color":3},
         "text":"ok?","spoken":true,"sessions":["herdr:1.2"],"lines":[4],"attachments":["/a"],
         "request":{"token":"tk","item":7,"kind":"question","options":["a","b"],"typed":true,"outcome":"x","job":"j"},"read":true}
        """#
        let e = try JSONDecoder().decode(ChatEntry.self, from: Data(json.utf8))
        XCTAssertEqual(e.who.color, 3)
        XCTAssertEqual(e.request?.options, ["a", "b"])
        XCTAssertEqual(e.lines, [4])
        XCTAssertTrue(e.read)
    }

    func testAnOlderEntryNotLoadedIsDroppedWhileThereIsMoreAndNeverShownTwice() async {
        let client = FakeChatClient((0..<60).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        var old = entry(3)
        old.text = "answered"
        model.apply(old)
        XCTAssertEqual(model.entries.count, 50)
        await model.loadOlder()
        XCTAssertEqual(model.entries.count, 60)
        XCTAssertEqual(Set(model.entries.map(\.id)).count, 60)
    }

    func testAnOlderEntryGoesInByTimeWhenThereIsNoMore() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(2), entry(4)]))
        await model.load()
        model.apply(entry(1))
        model.apply(entry(3))
        XCTAssertEqual(model.entries.map(\.id), ["e1", "e2", "e3", "e4"])
    }

    func testLoadOlderSkipsEntriesAlreadyShown() async {
        let client = FakeChatClient((0..<60).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        await model.loadOlder()
        XCTAssertEqual(model.entries.map(\.id), (0..<60).map { "e\($0)" })
    }

    func testLoadFetchesTheBadge() async {
        let client = FakeChatClient()
        client.setBadge(ChatBadge(needs: 2, unread: 5))
        let model = ChatThreadModel(client: client)
        await model.load()
        XCTAssertEqual(model.unread, 5)
    }

    func testClearCallsTheCoreAndResetEmptiesTheThread() async {
        let client = FakeChatClient((0..<3).map { entry($0) })
        let model = ChatThreadModel(client: client)
        await model.load()
        await model.clear()
        XCTAssertEqual(client.clears, 1)
        client.setFailing(true)
        await model.reset()
        XCTAssertTrue(model.entries.isEmpty, "a failed reload after Clear shows nothing")
        XCTAssertNotNil(model.error)
    }

    func testAnEventDuringLoadIsKept() async {
        let client = FakeChatClient((0..<3).map { entry($0) })
        let model = ChatThreadModel(client: client)
        client.whileLoading = {
            DispatchQueue.main.sync { MainActor.assumeIsolated { model.apply(ChatThreadModelTests.event7) } }
        }
        await model.load()
        XCTAssertEqual(model.entries.map(\.id), ["e0", "e1", "e2", "e7"])
    }

    func testAFailedSendDoesNotOverwriteANewTarget() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        model.replyTo = ChatWho(kind: "session", name: "a", label: "a")
        client.setFailing(true)
        async let sending: Void = model.send("x")
        model.replyTo = ChatWho(kind: "session", name: "b", label: "b")
        await sending
        XCTAssertEqual(model.replyTo?.name, "b")
    }

    func testAReplacedPixieEntryDoesNotEndThinking() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(1)]))
        await model.load()
        await model.send("go")
        model.apply(entry(1))
        XCTAssertTrue(model.thinking)
    }
}
