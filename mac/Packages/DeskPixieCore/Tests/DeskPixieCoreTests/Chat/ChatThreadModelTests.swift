import XCTest
@testable import DeskPixieCore

/// A thread like the core's: newest first pages, an unknown `before` gives none, a refused
/// call throws.
final class FakeChatClient: ChatClient, @unchecked Sendable {
    struct Sent: Equatable { let text: String; let session: String? }
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
    func send(_ text: String, session: String?) throws {
        try check()
        lock.withLock { gate }?.wait()
        if let why = lock.withLock({ () -> String? in defer { _failNext = nil }; return _failNext }) {
            throw DeskPixieEngineError.callFailed(why)
        }
        if let why = lock.withLock({ () -> String? in defer { _refuse = nil }; return _refuse }) {
            throw DeskPixieEngineError.refused(why)
        }
        lock.withLock { _sent.append(Sent(text: text, session: session)) }
    }
    func decide(token: String, allow: Bool) throws { try check() }
    func choose(token: String, option: Int) throws { try check() }
    struct Picked: Equatable { let token: String; let options: [Int] }
    private var _picked: [Picked] = []
    var picked: [Picked] { lock.withLock { _picked } }
    func pick(token: String, options: [Int]) throws {
        try check()
        lock.withLock { _picked.append(Picked(token: token, options: options)) }
    }
    private var _dismissed: [UInt64] = []
    var dismissed: [UInt64] { lock.withLock { _dismissed } }
    func dismiss(item: UInt64) throws {
        try check()
        lock.withLock { _dismissed.append(item) }
    }
    struct Typed: Equatable { let token: String; let text: String }
    private var _typed: [Typed] = []
    var typed: [Typed] { lock.withLock { _typed } }
    func typedAnswer(token: String, text: String) throws {
        try check()
        lock.withLock { _typed.append(Typed(token: token, text: text)) }
    }
    private var _chatted: [String] = []
    var chatted: [String] { lock.withLock { _chatted } }
    func chatAbout(token: String) throws {
        try check()
        lock.withLock { _chatted.append(token) }
    }
    func stop() throws { try check() }
    private var _interrupted: [String] = []
    var interrupted: [String] { lock.withLock { _interrupted } }
    /// The next interrupt is refused by the core, in these words.
    private var _refuseInterrupt: String?
    func refuseNextInterrupt(_ why: String) { lock.withLock { _refuseInterrupt = why } }
    /// `interrupt` waits here, until `releaseInterrupt()`.
    private var interruptGate: DispatchSemaphore?
    func holdInterrupt() { lock.withLock { interruptGate = DispatchSemaphore(value: 0) } }
    func releaseInterrupt() {
        lock.withLock { () -> DispatchSemaphore? in defer { interruptGate = nil }; return interruptGate }?.signal()
    }
    func interrupt(name: String) throws {
        try check()
        lock.withLock { interruptGate }?.wait()
        // The core marks its own sentences "refused" (agent.interrupt, as chat.send).
        if let why = lock.withLock({ () -> String? in defer { _refuseInterrupt = nil }; return _refuseInterrupt }) {
            throw DeskPixieEngineError.refused(why)
        }
        lock.withLock { _interrupted.append(name) }
    }
    private var _recipients: [ChatRecipient] = []
    func setRecipients(_ r: [ChatRecipient]) { lock.withLock { _recipients = r } }
    func recipients() throws -> [ChatRecipient] {
        try check()
        let asked = lock.withLock { _recipientCalls += 1; return _recipients }
        lock.withLock { recipientsGate }?.wait()
        return asked
    }
    /// `recipients()` waits here, answering with the list as it was when asked, until `releaseRecipients()`.
    private var recipientsGate: DispatchSemaphore?
    private var _recipientCalls = 0
    var recipientCalls: Int { lock.withLock { _recipientCalls } }
    func holdRecipients() { lock.withLock { recipientsGate = DispatchSemaphore(value: 0) } }
    func releaseRecipients() {
        lock.withLock { () -> DispatchSemaphore? in defer { recipientsGate = nil }; return recipientsGate }?.signal()
    }
    /// The next send fails as a call does (`callFailed`), not as a refusal.
    private var _failNext: String?
    func failNextSend(_ why: String) { lock.withLock { _failNext = why } }
    /// The next send is refused by the core, in these words (then sends go again).
    private var _refuse: String?
    func refuseNextSend(_ why: String) { lock.withLock { _refuse = why } }
    /// Sends wait here until `release()`: one is still on its way while the test goes on.
    private var gate: DispatchSemaphore?
    func holdSends() { lock.withLock { gate = DispatchSemaphore(value: 0) } }
    func release() { lock.withLock { () -> DispatchSemaphore? in defer { gate = nil }; return gate }?.signal() }
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
        await model.send("hi")
        XCTAssertEqual(client.sent, [.init(text: "hi", session: "herdr:1.2")])
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
        XCTAssertEqual(ChatThreadModel.header(ChatWho(kind: "pixie")), "Pixie")
        XCTAssertEqual(ChatThreadModel.header(ChatWho(kind: "coordinator")), "Coordinator")
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
        XCTAssertFalse(r.multi)
        let b = try JSONDecoder().decode(ChatBadge.self, from: Data(#"{"needs":1,"unread":2}"#.utf8))
        XCTAssertNil(b.preview)
    }

    func testAFullEntryDecodes() throws {
        let json = #"""
        {"id":"req7","at":9,"who":{"kind":"session","name":"herdr:1.2","label":"api","yours":true,"status":"asking","color":3},
         "text":"ok?","spoken":true,"sessions":["herdr:1.2"],"lines":[4],"attachments":["/a"],
         "request":{"token":"tk","item":7,"kind":"question","options":["a","b"],"typed":true,"multi":true,"outcome":"x","job":"j"},"read":true}
        """#
        let e = try JSONDecoder().decode(ChatEntry.self, from: Data(json.utf8))
        XCTAssertEqual(e.who.color, 3)
        XCTAssertEqual(e.request?.options, ["a", "b"])
        XCTAssertEqual(e.request?.multi, true)
        XCTAssertEqual(e.lines, [4])
        XCTAssertTrue(e.read)
    }

    /// #364: the options ticked go to the core together.
    func testPicksReachTheClient() async {
        let client = FakeChatClient([])
        let model = ChatThreadModel(client: client)
        await model.pick(token: "tk", options: [1, 3])
        XCTAssertEqual(client.picked, [FakeChatClient.Picked(token: "tk", options: [1, 3])])
    }

    /// #388: Dismiss goes to the core with the request's line, and shows only on an open
    /// question the core marked dismissible.
    func testDismissReachesTheClientAndShowsOnlyWhenOffered() async {
        let client = FakeChatClient([])
        let model = ChatThreadModel(client: client)
        await model.dismiss(item: 42)
        XCTAssertEqual(client.dismissed, [42])

        var asks = ask(5, kind: "question", token: "")
        XCTAssertFalse(ChatThreadModel.offersDismiss(asks), "not marked")
        asks.request?.dismissible = true
        XCTAssertTrue(ChatThreadModel.offersDismiss(asks))
        asks.request?.outcome = "Dismissed"
        XCTAssertFalse(ChatThreadModel.offersDismiss(asks), "no longer open")
        let json = #"{"id":"req7","at":1,"who":{"kind":"session","name":"api"},"text":"Want me to?","request":{"item":7,"kind":"question","dismissible":true}}"#
        let decoded = try? JSONDecoder().decode(ChatEntry.self, from: Data(json.utf8))
        XCTAssertEqual(decoded?.request?.dismissible, true)
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
        async let sending = model.send("x")
        model.replyTo = ChatWho(kind: "session", name: "b", label: "b")
        _ = await sending
        XCTAssertEqual(model.replyTo?.name, "b")
    }

    func testAReplacedPixieEntryDoesNotEndThinking() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(1)]))
        await model.load()
        await model.send("go")
        model.apply(entry(1))
        XCTAssertTrue(model.thinking)
    }

    // MARK: The window (#302, Task 8)

    private func ask(_ n: Int, kind: String, token: String = "tk", options: [String] = [], typed: Bool = false,
                     who: String = "herdr:1.2", label: String = "docs") -> ChatEntry {
        var e = entry(n, kind: "session", name: who, label: label)
        e.id = "req\(n)"
        e.request = ChatRequest(token: token, item: UInt64(n), kind: kind, options: options, typed: typed)
        return e
    }

    // MARK: The terminal button (Task 8d)

    func testASessionOfYoursOpensItsOwnTab() {
        var e = entry(1, kind: "session", name: "pix:api-server", label: "api-server")
        e.who.yours = true
        XCTAssertEqual(ChatThreadModel.terminalTarget(for: e),
                       ChatTerminalTarget(call: .focusSession, name: "pix:api-server", label: "Open api-server's terminal"))
        XCTAssertEqual(ChatThreadModel.terminalTarget(for: ask(2, kind: "question", who: "pix:docs"))?.call, .focusAgent,
                       "not yours: Pixie's pane")
    }

    func testAWorkerWithAJobOpensThroughItsJob() {
        var e = ask(1, kind: "permission", who: "w-fix-login", label: "fix-login")
        e.request?.job = "j7"
        XCTAssertEqual(ChatThreadModel.terminalTarget(for: e),
                       ChatTerminalTarget(call: .focusJob, name: "w-fix-login", job: "j7", label: "Open fix-login's terminal"))
    }

    func testAWorkerWithNoJobOpensItsPane() {
        let e = entry(1, kind: "session", name: "w-fix-login", label: "fix-login")
        XCTAssertEqual(ChatThreadModel.terminalTarget(for: e),
                       ChatTerminalTarget(call: .focusAgent, name: "w-fix-login", label: "Open fix-login's terminal"))
    }

    func testTheCoordinatorOpensItsPane() {
        XCTAssertEqual(ChatThreadModel.terminalTarget(for: entry(1, kind: "coordinator")),
                       ChatTerminalTarget(call: .focusAgent, name: "coordinator", label: "Open Coordinator's terminal"))
    }

    func testPixieAndYouHaveNoTerminal() {
        XCTAssertNil(ChatThreadModel.terminalTarget(for: entry(1, kind: "pixie")))
        XCTAssertNil(ChatThreadModel.terminalTarget(for: entry(2, kind: "you")))
    }

    func testAnEntryWithNoAddressOrJobHasNoTerminal() {
        XCTAssertNil(ChatThreadModel.terminalTarget(for: entry(1, kind: "session", label: "somebody")))
        var e = ask(2, kind: "question", who: "")
        e.request?.job = ""
        XCTAssertNil(ChatThreadModel.terminalTarget(for: e))
    }

    func testWhatACardOffers() {
        let allow = ask(1, kind: "permission")
        XCTAssertTrue(ChatThreadModel.offersDecision(allow))
        XCTAssertFalse(ChatThreadModel.offersOptions(allow))
        XCTAssertFalse(ChatThreadModel.offersReply(allow))

        let menu = ask(2, kind: "question", options: ["a", "b"], typed: true)
        XCTAssertFalse(ChatThreadModel.offersDecision(menu))
        XCTAssertTrue(ChatThreadModel.offersOptions(menu))
        XCTAssertTrue(ChatThreadModel.offersReply(menu), "a typed answer")

        XCTAssertTrue(ChatThreadModel.offersChatAbout(menu), "a menu that takes words also takes Chat about this")
        XCTAssertFalse(ChatThreadModel.offersChatAbout(allow))

        let fixed = ask(3, kind: "question", options: ["a"])
        XCTAssertFalse(ChatThreadModel.offersReply(fixed), "only its options")
        XCTAssertFalse(ChatThreadModel.offersChatAbout(fixed), "a menu that takes no words has no chat option")

        let noButtons = ask(4, kind: "question", token: "", options: ["a"])
        XCTAssertFalse(ChatThreadModel.offersOptions(noButtons), "no token, nothing to choose with")
        XCTAssertTrue(ChatThreadModel.offersReply(noButtons), "answered by a message to its session")

        // #364: a question of several picks ticks them instead, and takes no typed answer.
        var picks = ask(6, kind: "question", options: ["a", "b"], typed: true)
        picks.request?.multi = true
        XCTAssertTrue(ChatThreadModel.offersPicks(picks))
        XCTAssertFalse(ChatThreadModel.offersOptions(picks))
        XCTAssertFalse(ChatThreadModel.offersReply(picks))
        XCTAssertFalse(ChatThreadModel.offersChatAbout(picks))
        XCTAssertFalse(ChatThreadModel.offersPicks(menu))

        var done = menu
        done.request?.outcome = "You answered: a"
        XCTAssertFalse(ChatThreadModel.isOpen(done))
        XCTAssertFalse(ChatThreadModel.offersOptions(done))
        XCTAssertFalse(ChatThreadModel.offersReply(done))
        XCTAssertFalse(ChatThreadModel.isOpen(entry(5)), "not a request")
    }

    func testPillCopy() {
        var push = ask(1, kind: "permission", label: "api-server")
        push.text = " git push origin fix-login\n"
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server wants to run git push origin fix-login")
        push.text = "  "
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server wants permission", "no text: the generic words")
        XCTAssertEqual(ChatThreadModel.pillLabel(ask(2, kind: "question")), "docs asks a question")
        push.text = "wants to run git push"
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server wants to run git push", "said once, not twice")
        push.text = "wants to edit main.go"
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server wants to edit main.go")
        push.text = "asks to read ~/.ssh"
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server asks to read ~/.ssh")
        push.text = "wantsomething"
        XCTAssertEqual(ChatThreadModel.pillLabel(push), "api-server wants to run wantsomething", "a whole word only")
    }

    func testAPlainSessionMessageOffersReply() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let update = entry(1, kind: "session", name: "herdr:1.2", label: "api")
        XCTAssertTrue(ChatThreadModel.offersSessionReply(update))
        XCTAssertFalse(ChatThreadModel.offersSessionReply(entry(2, kind: "session")), "no address to send to")
        XCTAssertFalse(ChatThreadModel.offersSessionReply(entry(3, kind: "coordinator", name: "x")))
        XCTAssertFalse(ChatThreadModel.offersSessionReply(ask(4, kind: "question")), "a card has its own Reply…")
        model.reply(to: update)
        XCTAssertEqual(model.placeholder, "Reply to api…")
        await model.send("rebase it")
        XCTAssertEqual(client.sent, [.init(text: "rebase it", session: "herdr:1.2")])
        XCTAssertTrue(client.typed.isEmpty)
    }

    func testItOpensAtTheOldestOfTheUnreadTheLoadCounted() async {
        // Like a seeded thread: older entries never marked read, the core counting the last two.
        let client = FakeChatClient((1...8).map { entry($0) })
        client.setBadge(ChatBadge(unread: 2))
        let model = ChatThreadModel(client: client)
        await model.load()
        XCTAssertEqual(model.unread, 2, "the count comes with the load")
        XCTAssertEqual(model.openTarget, "e7")
        model.applyBadge(ChatBadge(unread: 20))
        XCTAssertEqual(model.openTarget, "e1", "more than there are: the oldest")
        model.applyBadge(ChatBadge(unread: 0))
        XCTAssertEqual(model.openTarget, "e8")
    }

    func testASecondSendWhileOneIsOnItsWayIsRefused() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        XCTAssertTrue(model.canSend("", attachments: 1))
        XCTAssertFalse(model.canSend("  ", attachments: 0))
        client.holdSends()
        let first = Task { await model.send("look") }
        await waitFor { MainActor.assumeIsolated { model.sending } }
        let second = await model.send("look")
        XCTAssertFalse(second, "refused while the first is on its way")
        client.release()
        let a = await first.value
        XCTAssertTrue(a, "the first went")
        XCTAssertEqual(client.sent.count, 1, "the files go once")
        XCTAssertFalse(model.sending)
        XCTAssertTrue(model.canSend("again", attachments: 0))
        let again = await model.send("again")
        XCTAssertTrue(again)
        client.setFailing(true)
        let failed = await model.send("no")
        XCTAssertFalse(failed, "a failed send says so")
    }

    func testOnlyContentTallerThanTheViewportMovesOnTheOpeningScroll() {
        XCTAssertTrue(ChatThreadModel.scrollMoves(contentHeight: 900, viewportHeight: 600))
        XCTAssertFalse(ChatThreadModel.scrollMoves(contentHeight: 600, viewportHeight: 600))
        XCTAssertFalse(ChatThreadModel.scrollMoves(contentHeight: 120, viewportHeight: 600))
    }

    func testAViewAtTheBottomFollowsItsContentGrowing() {
        XCTAssertTrue(ChatThreadModel.keepsBottom(wasAtBottom: true, heightChanged: true))
        XCTAssertFalse(ChatThreadModel.keepsBottom(wasAtBottom: true, heightChanged: false), "you scrolled up")
        XCTAssertFalse(ChatThreadModel.keepsBottom(wasAtBottom: false, heightChanged: true), "reading further up")
    }

    /// Found by hand: Attach on the hint row put a chip above the box, the thread got shorter,
    /// and "↓ Latest" showed. At the bottom before, it stays at the bottom.
    func testTheBoxGrowingKeepsTheBottom() {
        XCTAssertTrue(ChatThreadModel.staysAtBottom(wasAtBottom: true, oldViewport: 600, newViewport: 560))
        XCTAssertTrue(ChatThreadModel.staysAtBottom(wasAtBottom: true, oldViewport: 600, newViewport: 640), "and shrinking")
        XCTAssertFalse(ChatThreadModel.staysAtBottom(wasAtBottom: false, oldViewport: 600, newViewport: 560),
                       "scrolled up: left where you were")
        XCTAssertFalse(ChatThreadModel.staysAtBottom(wasAtBottom: true, oldViewport: 600, newViewport: 600), "no change")
    }

    func testTheBottomIsSeenOnlyOnceOpenedOnScreenAndInView() {
        XCTAssertTrue(ChatThreadModel.isAtBottom(sentinelMaxY: 600, viewportHeight: 600))
        XCTAssertTrue(ChatThreadModel.isAtBottom(sentinelMaxY: 606, viewportHeight: 600), "within the tolerance")
        XCTAssertFalse(ChatThreadModel.isAtBottom(sentinelMaxY: 640, viewportHeight: 600))
        XCTAssertFalse(ChatThreadModel.isAtBottom(sentinelMaxY: .infinity, viewportHeight: 600), "not laid out")
        XCTAssertFalse(ChatThreadModel.isAtBottom(sentinelMaxY: 10, viewportHeight: 0), "no viewport yet")
        XCTAssertTrue(ChatThreadModel.marksRead(opened: true, visible: true, atBottom: true))
        XCTAssertFalse(ChatThreadModel.marksRead(opened: false, visible: true, atBottom: true), "before the opening scroll")
        XCTAssertFalse(ChatThreadModel.marksRead(opened: true, visible: false, atBottom: true), "hidden or minimized")
        XCTAssertFalse(ChatThreadModel.marksRead(opened: true, visible: true, atBottom: false))
    }

    func testReplyToATypedQuestionSendsATypedAnswer() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let q = ask(1, kind: "question", options: ["a"], typed: true)
        model.applyNeeds([q])
        model.reply(to: q)
        XCTAssertEqual(model.replyTo?.label, "docs")
        XCTAssertEqual(model.placeholder, "Reply to docs…")
        await model.send("use the plain list")
        XCTAssertEqual(client.typed, [.init(token: "tk", text: "use the plain list")])
        XCTAssertTrue(client.sent.isEmpty)
        XCTAssertNil(model.replyTo)
        XCTAssertFalse(model.thinking)
    }

    /// "Chat about this" on a card reaches the core as `agent.chatAbout` with the card's token.
    func testChatAboutThisGoesToTheCore() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        await model.chatAbout(token: "tk")
        XCTAssertEqual(client.chatted, ["tk"])
    }

    /// The core sends a typed answer as `agent.typedAnswer`, without the one-recipient rule
    /// (#302 review M4): the box doesn't hold it back for an @name either.
    func testATypedAnswerSkipsTheMentionRule() async {
        let client = FakeChatClient()
        let api = ChatRecipient(name: "api-codex", label: "api-server")
        client.setRecipients([api, ChatRecipient(name: "herdr:1.2", label: "docs")])
        let model = ChatThreadModel(client: client)
        await model.loadRecipients()
        let q = ask(1, kind: "question", options: ["a"], typed: true)
        model.applyNeeds([q])
        model.reply(to: q)
        let text = "same as @api-codex did"
        XCTAssertNil(model.check(text), "answering: no rule")
        XCTAssertTrue(model.canSend(text, attachments: 0))
        await model.send(text)
        XCTAssertEqual(client.typed, [.init(token: "tk", text: text)])

        model.applyNeeds([])
        model.reply(to: q) // answered meanwhile: a message to its session, and the rule holds
        XCTAssertEqual(model.check(text), "This goes to docs only. Remove @api-codex, or pick them instead.")
    }

    /// A `chat.recipients` answer asked before the last event is older than it (#302 review M1).
    func testAnOlderRecipientsAnswerDoesNotOverwriteAnEvent() async {
        let client = FakeChatClient()
        let old = ChatRecipient(name: "old-claude", label: "old")
        let new = ChatRecipient(name: "new-claude", label: "new")
        client.setRecipients([old])
        client.holdRecipients()
        let model = ChatThreadModel(client: client)
        let asking = Task { await model.loadRecipients() }
        await waitFor { client.recipientCalls == 1 } // the call is on its way, with [old]
        model.applyRecipients([new])
        client.releaseRecipients()
        await asking.value
        XCTAssertEqual(model.recipients, [new], "the event is newer than the answer")

        client.setRecipients([old])
        await model.loadRecipients()
        XCTAssertEqual(model.recipients, [old], "with no event meanwhile, the answer is the list")
    }

    /// Only a refusal shows as one; any other failed send is a failure (#302 review M2).
    func testAFailedSendIsNotARefusal() async {
        let client = FakeChatClient()
        let api = ChatRecipient(name: "api-codex", label: "api-server")
        client.setRecipients([api])
        let model = ChatThreadModel(client: client)
        await model.loadRecipients()
        model.choose(api)
        client.failNextSend("Delegation is off (Coordinator: herdr not found).")
        let went = await model.send("ship it")
        XCTAssertFalse(went)
        XCTAssertNil(model.refusal)
        XCTAssertNil(model.notice(for: "ship it"), "nothing under the box")
        XCTAssertEqual(model.error, "Couldn't reach Pixie. Try again.")
        XCTAssertEqual(model.replyTo, api.who, "the target is back")

        client.refuseNextSend("api-server isn't running anymore.")
        await model.send("ship it")
        XCTAssertEqual(model.refusal, "api-server isn't running anymore.")
    }

    func testReplyToAQuestionWithoutATokenMessagesItsSession() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let q = ask(1, kind: "question", token: "")
        model.applyNeeds([q])
        model.reply(to: q)
        await model.send("yes")
        XCTAssertEqual(client.sent, [.init(text: "yes", session: "herdr:1.2")])
        XCTAssertTrue(client.typed.isEmpty)
    }

    func testAQuestionAnsweredMeanwhileFallsBackToItsSession() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let q = ask(1, kind: "question", options: ["a"], typed: true)
        model.applyNeeds([q])
        model.reply(to: q)
        model.applyNeeds([])
        await model.send("late")
        XCTAssertTrue(client.typed.isEmpty, "its token is gone")
        XCTAssertEqual(client.sent.map(\.session), ["herdr:1.2"])
    }

    func testCancelReplyGoesBackToPixie() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let q = ask(1, kind: "question", options: ["a"], typed: true)
        model.applyNeeds([q])
        model.reply(to: q)
        model.cancelReply()
        XCTAssertNil(model.replyTo)
        XCTAssertEqual(model.placeholder, "Message Pixie…")
        await model.send("hi")
        XCTAssertTrue(client.typed.isEmpty)
        XCTAssertEqual(client.sent.map(\.session), [nil])
        XCTAssertTrue(model.thinking)
    }

    func testAFailedTypedAnswerKeepsTheReply() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        let q = ask(1, kind: "question", options: ["a"], typed: true)
        model.applyNeeds([q])
        model.reply(to: q)
        client.setFailing(true)
        await model.send("x")
        XCTAssertNotNil(model.error)
        XCTAssertEqual(model.replyTo?.label, "docs")
        client.setFailing(false)
        await model.send("x")
        XCTAssertEqual(client.typed, [.init(token: "tk", text: "x")], "the token came back with the target")
    }

    func testATimeDividerGoesBetweenMessagesMoreThanFiveMinutesApart() {
        let a = ChatEntry(id: "a", at: 1_000_000, who: ChatWho(kind: "you"), text: "")
        var b = a
        b.at = a.at + 300_000
        var c = a
        c.at = b.at + 300_001
        XCTAssertTrue(ChatThreadModel.startsNewTime(after: nil, a), "the first one has its time")
        XCTAssertFalse(ChatThreadModel.startsNewTime(after: a, b))
        XCTAssertTrue(ChatThreadModel.startsNewTime(after: b, c))
    }

    func testItOpensAtTheOldestUnreadElseTheNewest() async {
        var mine = entry(1, kind: "you")
        mine.read = false
        var seen = entry(2)
        seen.read = true
        let fresh = entry(3)
        let model = ChatThreadModel(client: FakeChatClient([mine, seen, fresh, entry(4)]))
        await model.load()
        XCTAssertEqual(model.openTarget, "e4", "nothing unread: the newest")
        model.applyBadge(ChatBadge(unread: 2))
        XCTAssertEqual(model.openTarget, "e3", "your own words are never unread")
    }

    func testASessionsLabelIsItsNewestEntrys() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(1, kind: "session", name: "h:1", label: "old"),
                                                            entry(2, kind: "session", name: "h:1", label: "api")]))
        await model.load()
        XCTAssertEqual(model.who(forSession: "h:1").label, "api")
        XCTAssertEqual(model.who(forSession: "h:9").label, "h:9")
    }

    // MARK: Live text (Task 8b)

    func testLiveReplacesByWho() {
        let model = ChatThreadModel(client: FakeChatClient())
        model.applyLive(ChatLive(who: "you", text: "what"))
        model.applyLive(ChatLive(who: "you", text: "what time"))
        model.applyLive(ChatLive(who: "pixie", text: "Ten"))
        model.applyLive(ChatLive(who: "coordinator", text: "Reading the logs.", job: "j1"))
        model.applyLive(ChatLive(who: "coordinator", text: "Looking.", job: "j2"))
        XCTAssertEqual(model.liveText("you"), "what time")
        XCTAssertEqual(model.liveText("pixie"), "Ten")
        XCTAssertEqual(model.live.count, 4, "one line per who, and per job for the coordinator")
        XCTAssertEqual(model.coordinatorStep, "Looking.")
        model.applyLive(ChatLive(who: "coordinator", text: "Running the tests.", job: "j1"))
        XCTAssertEqual(model.coordinatorStep, "Running the tests.", "the latest step shows")
        XCTAssertEqual(model.live.count, 4)
        model.applyLive(ChatLive(who: "you", text: ""))
        XCTAssertNil(model.liveText("you"), "an empty text clears")
        XCTAssertEqual(model.liveText("pixie"), "Ten")
        model.applyLive(ChatLive(who: "coordinator", text: "", job: "j1"))
        XCTAssertEqual(model.coordinatorStep, "Looking.", "clearing one job leaves the other")
    }

    func testThinkingShowsTheCoordinatorsStep() async {
        let model = ChatThreadModel(client: FakeChatClient())
        XCTAssertFalse(model.showsThinking)
        await model.send("do it")
        XCTAssertTrue(model.showsThinking)
        XCTAssertEqual(model.thinkingLabel, "Thinking…")
        model.applyLive(ChatLive(who: "coordinator", text: "Reading the logs.", job: "j1"))
        XCTAssertEqual(model.thinkingLabel, "Thinking… · Reading the logs.")
        model.apply(entry(2, kind: "coordinator")) // an answer ends the wait; the core clears steps
        XCTAssertFalse(model.thinking)
        XCTAssertTrue(model.showsThinking, "a step shows Thinking… even when not waiting on a send")
        model.applyLive(ChatLive(who: "coordinator", text: "", job: "j1")) // the core's clear (Stop too)
        XCTAssertFalse(model.showsThinking)
    }

    func testAFinalEntryClearsItsLiveLine() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(1)]))
        await model.load()
        model.applyLive(ChatLive(who: "you", text: "what time"))
        model.applyLive(ChatLive(who: "pixie", text: "Ten"))
        model.applyLive(ChatLive(who: "coordinator", text: "Looking.", job: "j1"))
        model.applyLive(ChatLive(who: "coordinator", text: "Reading.", job: "j2"))
        model.apply(entry(2, kind: "session"))
        XCTAssertEqual(model.live.count, 4, "a session's entry clears nothing")
        model.apply(entry(3, kind: "you"))
        XCTAssertNil(model.liveText("you"))
        XCTAssertEqual(model.liveText("pixie"), "Ten")
        model.apply(entry(1)) // the same entry again, unchanged: not Pixie's new words
        XCTAssertEqual(model.liveText("pixie"), "Ten")
        var grown = entry(1)
        grown.text = "Ten past three."
        model.apply(grown)
        XCTAssertNil(model.liveText("pixie"), "Pixie's words grown in place are final too")
        model.apply(entry(4, kind: "coordinator"))
        XCTAssertEqual(model.coordinatorStep, "Reading.", "the core clears a job's step, not a coordinator entry")
        XCTAssertEqual(model.live.count, 2)
    }

    func testClearLiveEmptiesAndAReloadKeepsALineThatCameAfter() async {
        let model = ChatThreadModel(client: FakeChatClient([entry(1)]))
        model.applyLive(ChatLive(who: "you", text: "hey"))
        model.clearLive() // chat.reset, as it comes
        XCTAssertTrue(model.live.isEmpty)
        model.applyLive(ChatLive(who: "pixie", text: "Hi")) // came after the reset
        await model.reset()
        XCTAssertEqual(model.liveText("pixie"), "Hi", "the reload doesn't wipe a line that came after the reset")
    }
}
