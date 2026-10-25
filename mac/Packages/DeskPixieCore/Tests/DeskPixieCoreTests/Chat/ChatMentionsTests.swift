import XCTest
@testable import DeskPixieCore

/// The @ list in the chat box and the one-recipient rule (#302, Task 8e). The rule's cases
/// match the core's (`core/chat/recipients_test.go`).
final class ChatMentionsTests: XCTestCase {
    static let docs = ChatRecipient(name: "deskpixie-claude", label: "chat window")
    static let api = ChatRecipient(name: "api-codex", label: "api-server")
    static let worker = ChatRecipient(name: "fix-login", label: "fix-login", kind: "worker", yours: false)
    static let all = [docs, api, worker]

    // MARK: Finding the @ being typed

    func testAtTheStartOfTheText() {
        XCTAssertEqual(MentionQuery.active(in: "@", caret: 1), MentionQuery(start: 0, query: ""))
        XCTAssertEqual(MentionQuery.active(in: "@ap", caret: 3), MentionQuery(start: 0, query: "ap"))
    }

    func testAfterASpace() {
        XCTAssertEqual(MentionQuery.active(in: "ask @fi", caret: 7), MentionQuery(start: 4, query: "fi"))
        XCTAssertEqual(MentionQuery.active(in: "one\n@", caret: 5), MentionQuery(start: 4, query: ""))
    }

    func testNotInsideAWord() {
        XCTAssertNil(MentionQuery.active(in: "a@b", caret: 3))
        XCTAssertNil(MentionQuery.active(in: "me@api-codex.com", caret: 16))
    }

    func testASpaceClosesIt() {
        XCTAssertNil(MentionQuery.active(in: "@api ", caret: 5))
        XCTAssertNil(MentionQuery.active(in: "no at here", caret: 10))
        XCTAssertNil(MentionQuery.active(in: "", caret: 0))
    }

    func testOnlyUpToTheCaret() {
        XCTAssertEqual(MentionQuery.active(in: "@api-codex", caret: 2), MentionQuery(start: 0, query: "a"))
        XCTAssertNil(MentionQuery.active(in: "@api hello", caret: 99), "clamped to the end, after a space")
    }

    // MARK: The list

    func testPixieThenEveryone() {
        XCTAssertEqual(MentionQuery(start: 0, query: "").candidates(Self.all),
                       [.pixie, .recipient(Self.docs), .recipient(Self.api), .recipient(Self.worker)])
        XCTAssertEqual(MentionCandidate.pixie.detail, "Message Pixie")
        XCTAssertEqual(MentionCandidate.recipient(Self.api).detail, "@api-codex")
    }

    func testFilterPrefixFirstThenContainsInAnyCase() {
        XCTAssertEqual(MentionQuery(start: 0, query: "A").candidates(Self.all), [.recipient(Self.api), .recipient(Self.docs)],
                       "api-server starts with it, so it comes before chat window, which only contains it")
        XCTAssertEqual(MentionQuery(start: 0, query: "pi").candidates(Self.all),
                       [.pixie, .recipient(Self.docs), .recipient(Self.api)])
        XCTAssertEqual(MentionQuery(start: 0, query: "log").candidates(Self.all), [.recipient(Self.worker)])
        XCTAssertEqual(MentionQuery(start: 0, query: "api-c").candidates(Self.all), [.recipient(Self.api)], "by its @name")
    }

    func testNoMatch() {
        let q = MentionQuery(start: 0, query: "zed")
        XCTAssertEqual(q.candidates(Self.all), [])
        XCTAssertEqual(q.emptyLabel(), "No one called zed")
    }

    // MARK: Picking

    func testPickTakesTheAtOutAndSetsTheTarget() {
        let text = "please @ap"
        let q = MentionQuery.active(in: text, caret: 10)!
        let (out, target) = q.pick(.recipient(Self.api), in: text, caret: 10, previous: nil)
        XCTAssertEqual(out, "please ")
        XCTAssertEqual(target, Self.api)
    }

    func testAPickReplacesAnEarlierTargetAndItsMention() {
        let text = "@api-codex ship it @fi"
        let q = MentionQuery.active(in: text, caret: text.count)!
        let (out, target) = q.pick(.recipient(Self.worker), in: text, caret: text.count, previous: Self.api)
        XCTAssertEqual(out, "ship it ")
        XCTAssertEqual(target, Self.worker)
        XCTAssertNil(ChatRecipients.check(out, target: target?.name, label: "", recipients: Self.all),
                     "the text names only the new target")
    }

    func testPickingPixieClearsTheTarget() {
        let text = "tell @API-CODEX. @pi"
        let q = MentionQuery.active(in: text, caret: text.count)!
        let (out, target) = q.pick(.pixie, in: text, caret: text.count, previous: Self.api)
        XCTAssertNil(target)
        XCTAssertEqual(out, "tell. ", "its mention went too, any case; the full stop stays")
    }

    func testPickingTheSameTargetKeepsItsMention() {
        let text = "@api-codex hi @api"
        let q = MentionQuery.active(in: text, caret: text.count)!
        XCTAssertEqual(q.pick(.recipient(Self.api), in: text, caret: text.count, previous: Self.api).text,
                       "@api-codex hi ")
    }

    func testAPickTakesOutTheOldTargetsLabelToo() {
        let text = "@API-SERVER ship it @fi"
        let q = MentionQuery.active(in: text, caret: text.count)!
        XCTAssertEqual(q.pick(.recipient(Self.worker), in: text, caret: text.count, previous: Self.api).text, "ship it ")
    }

    @MainActor
    func testThePickIsTheModelsOneTarget() {
        let model = ChatThreadModel(client: FakeChatClient())
        model.choose(Self.api)
        XCTAssertEqual(model.replyTo, Self.api.who)
        model.choose(Self.worker)
        XCTAssertEqual(model.replyTo?.name, "fix-login", "only ever one")
        model.choose(nil)
        XCTAssertNil(model.replyTo, "Pixie clears it")
    }

    // MARK: One recipient per message

    func testTheRule() {
        let cases: [(String, String?, String, String?)] = [
            ("what's up?", nil, "", nil),
            ("ping @nobody", nil, "", nil),
            ("write to me@api-codex.com", nil, "", nil),
            ("@api-codex ship it", nil, "", "Pick @api-codex from the list to send it to them."),
            ("@Api-Codex ship it", nil, "", "Pick @Api-Codex from the list to send it to them."),
            ("@api-server ship it", nil, "", "Pick @api-server from the list to send it to them."),
            ("@chat is two words", nil, "", nil),
            ("@api-server ship it", "api-codex", "", nil),
            ("@fix-login and @api-codex", nil, "", "Send to one at a time: pick @fix-login or @api-codex."),
            ("@fix-login @fix-login", nil, "", "Pick @fix-login from the list to send it to them."),
            ("ship it", "api-codex", "", nil),
            ("@api-codex ship it", "api-codex", "", nil),
            ("ask @api-codex.", "api-codex", "", nil),
            ("cc @nobody", "api-codex", "", nil),
            ("tell @fix-login too", "api-codex", "", "This goes to api-server only. Remove @fix-login, or pick them instead."),
            ("x@fix-login", "api-codex", "", nil),
            ("@api-codex2 hi", nil, "", nil),
            ("go on", "fix-login", "", nil),
            ("hi", "old-claude", "docs", "docs isn't running anymore."),
            ("hi", "w9", "", "w9 isn't running anymore."),
            ("hi", "", "", "That session isn't running anymore."),
        ]
        for (text, target, label, want) in cases {
            XCTAssertEqual(ChatRecipients.check(text, target: target, label: label, recipients: Self.all), want,
                           "\(text) → \(target ?? "Pixie")")
        }
        XCTAssertNil(ChatRecipients.check("hi", target: "w9", label: "", recipients: [], knowsRecipients: false),
                     "before the list has come, the core decides")
    }

    /// x's label is beta's address, and y and z share a label. The same table is in the core's
    /// `TestAnAddressFirstThenALabel`.
    func testAnAddressFirstThenALabel() {
        let shadow = [ChatRecipient(name: "x", label: "beta"), ChatRecipient(name: "beta", label: "b-session"),
                      ChatRecipient(name: "y", label: "dup"),
                      ChatRecipient(name: "z", label: "dup", kind: "worker", yours: false)]
        let cases: [(String, String?, [String])] = [
            ("@x @beta", nil, ["x", "beta"]),
            ("@beta", nil, ["beta"]),
            ("@beta", "x", ["beta"]),
            ("@b-session", nil, ["beta"]),
            ("@dup", nil, ["y"]),
            ("@dup", "z", ["z"]),
            ("@dup @y", nil, ["y"]),
            ("@dup @y", "z", ["z", "y"]),
            ("@y @dup", nil, ["y"]),
        ]
        for (text, target, want) in cases {
            XCTAssertEqual(ChatRecipients.mentions(in: text, shadow, target: target).map(\.recipient.name), want,
                           "\(text) to \(target ?? "-")")
        }
        let checks: [(String, String?, String?)] = [
            ("@x @beta", nil, "Send to one at a time: pick @x or @beta."),
            ("@beta hi", "x", "This goes to beta only. Remove @beta, or pick them instead."),
            ("@dup hi", "z", nil),
            ("@dup hi", "y", nil),
            ("@dup @y", "z", "This goes to dup only. Remove @y, or pick them instead."),
        ]
        for (text, target, want) in checks {
            XCTAssertEqual(ChatRecipients.check(text, target: target, label: "", recipients: shadow), want, text)
        }
    }

    @MainActor
    func testTheBoxSaysWhyAndSendIsOff() async {
        let client = FakeChatClient()
        client.setRecipients(Self.all)
        let model = ChatThreadModel(client: client)
        await model.loadRecipients()
        model.choose(Self.docs)
        let text = "and @api-codex too"
        XCTAssertEqual(model.notice(for: text), "This goes to chat window only. Remove @api-codex, or pick them instead.")
        XCTAssertFalse(model.canSend(text, attachments: 0), "Send is off")
        XCTAssertTrue(model.canSend("just you", attachments: 0))
        model.choose(nil)
        XCTAssertEqual(model.notice(for: "@api-codex hi"), "Pick @api-codex from the list to send it to them.")
        XCTAssertFalse(model.canSend("@api-codex hi", attachments: 1))
    }

    @MainActor
    func testATargetThatEndedIsSaid() async {
        let client = FakeChatClient()
        let model = ChatThreadModel(client: client)
        model.choose(Self.api)
        XCTAssertNil(model.notice(for: "hi"), "the list hasn't come: the core decides")
        model.applyRecipients([Self.docs])
        XCTAssertEqual(model.notice(for: "hi"), "api-server isn't running anymore.")
        XCTAssertFalse(model.canSend("hi", attachments: 0))
        model.applyRecipients(Self.all)
        XCTAssertNil(model.notice(for: "hi"))
    }

    @MainActor
    func testARefusalByTheCoreIsShownAndKeepsTheTarget() async {
        let client = FakeChatClient()
        client.setRecipients(Self.all)
        let model = ChatThreadModel(client: client)
        await model.loadRecipients()
        model.choose(Self.api)
        client.refuseNextSend("api-server isn't running anymore.")
        let went = await model.send("ship it", attachments: ["/tmp/a.png"])
        XCTAssertFalse(went, "the composer keeps the draft and the attachments when a send didn't go")
        XCTAssertEqual(model.notice(for: "ship it"), "api-server isn't running anymore.", "the core's words")
        XCTAssertNil(model.error, "not \"Couldn't reach Pixie\"")
        XCTAssertEqual(model.replyTo, Self.api.who, "the target is back")
        XCTAssertTrue(client.sent.isEmpty)
        model.draftChanged()
        XCTAssertNil(model.notice(for: "ship it"), "editing clears it")
        let again = await model.send("ship it")
        XCTAssertTrue(again)
        XCTAssertEqual(client.sent.last?.session, "api-codex")
    }

    func testRecipientsDecodeAsTheCoreSendsThem() throws {
        let json = #"{"name":"fix-login","label":"fix-login","kind":"worker","color":3}"#
        let r = try JSONDecoder().decode(ChatRecipient.self, from: Data(json.utf8))
        XCTAssertEqual(r, ChatRecipient(name: "fix-login", label: "fix-login", kind: "worker", yours: false, color: 3))
        XCTAssertEqual(r.who.kind, "session")
        XCTAssertFalse(r.who.yours)
    }
}
