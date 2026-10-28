import XCTest
@testable import DeskPixieCore

final class EngineChatClientTests: XCTestCase {
    private final class Recorder: CoreEngine, @unchecked Sendable {
        private let lock = NSLock()
        private var _calls: [(String, [String: Any])] = []
        var reply = "null"
        var calls: [(String, [String: Any])] { lock.withLock { _calls } }

        func call(_ method: String, _ args: any Encodable) throws -> Data {
            let data = try JSONEncoder().encode(args)
            let obj = (try JSONSerialization.jsonObject(with: data)) as? [String: Any] ?? [:]
            lock.withLock { _calls.append((method, obj)) }
            return Data(reply.utf8)
        }
        func hotkey() {}
        func configure(_ c: EngineConfig) throws {}
        func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool { true }
        func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) { (0, false) }
        func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {}
        func micFailed(_ reason: String) {}
        func pollEvent() -> EngineEvent? { nil }
        func packJSON() -> Data? { nil }
        func shutdown() {}
    }

    private func keys(_ o: [String: Any]) -> Set<String> { Set(o.keys) }

    func testChatClientSendsTheCoresArgs() throws {
        let e = Recorder()
        let c = EngineChatClient(engine: e)
        e.reply = #"{"job":"j1"}"#
        try c.send("hi", session: nil)
        try c.send("hi", session: "web-claude")
        try c.send("hi", session: "")
        e.reply = #"{"entries":[]}"#
        _ = try c.entries(before: nil, limit: 50)
        _ = try c.entries(before: "e9", limit: 50)
        e.reply = "null"
        try c.read(upTo: "e3")
        try c.decide(token: "t", allow: true)
        try c.choose(token: "t", option: 2)
        try c.pick(token: "t", options: [1, 3])
        try c.typedAnswer(token: "t", text: "x")
        try c.clear()
        try c.stop()

        let calls = e.calls
        XCTAssertEqual(calls.map(\.0), ["chat.send", "chat.send", "chat.send", "chat.entries", "chat.entries",
                                        "chat.read", "agent.decide", "agent.choose", "agent.pick", "agent.typedAnswer",
                                        "chat.clear", "chat.stop"])
        // The Go input structs' json names: text, session | before, limit | upTo | token, allow...
        // No attachments: the core sends the ones waiting and says which went (#394).
        XCTAssertEqual(keys(calls[0].1), ["text", "from"], "session is left out for Pixie")
        XCTAssertEqual(calls[0].1["from"] as? String, "window", "the window's rule applies")
        XCTAssertEqual(keys(calls[1].1), ["text", "session", "from"])
        XCTAssertEqual(calls[1].1["session"] as? String, "web-claude")
        XCTAssertEqual(keys(calls[2].1), ["text", "session", "from"], "an empty address goes, for the core to refuse")
        XCTAssertEqual(calls[2].1["session"] as? String, "")
        XCTAssertEqual(calls[3].1["before"] as? String, "")
        XCTAssertEqual(calls[3].1["limit"] as? Int, 50)
        XCTAssertEqual(calls[4].1["before"] as? String, "e9")
        XCTAssertEqual(calls[5].1 as NSDictionary, ["upTo": "e3"] as NSDictionary)
        XCTAssertEqual(calls[6].1 as NSDictionary, ["token": "t", "allow": true] as NSDictionary)
        XCTAssertEqual(calls[7].1 as NSDictionary, ["token": "t", "option": 2] as NSDictionary)
        XCTAssertEqual(calls[8].1 as NSDictionary, ["token": "t", "options": [1, 3]] as NSDictionary, "agent.pick (#364)")
        XCTAssertEqual(calls[9].1 as NSDictionary, ["token": "t", "text": "x"] as NSDictionary)
        XCTAssertTrue(calls[10].1.isEmpty)
        XCTAssertTrue(calls[11].1.isEmpty)
    }

    func testChatClientDecodesReplies() throws {
        let e = Recorder()
        let c = EngineChatClient(engine: e)
        e.reply = #"{"entries":[{"id":"a","at":5,"who":{"kind":"pixie"},"text":"hi","extra":1}]}"#
        XCTAssertEqual(try c.entries(before: nil, limit: 50).map(\.id), ["a"])
        XCTAssertEqual(try c.needs().map(\.text), ["hi"])
        e.reply = #"{"needs":2,"unread":3}"#
        XCTAssertEqual(try c.badge(), ChatBadge(needs: 2, unread: 3))
    }

    private func d(_ s: String) -> EngineEvent? { EngineEvent.decode(Data(s.utf8)) }

    func testDecodesChatEntryNeedsAndBadge() {
        let entry = #"{"id":"e1","at":1700,"who":{"kind":"session","name":"web","label":"Web","yours":true,"status":"waiting","color":3,"new":1},"text":"ok?","spoken":true,"sessions":["web"],"lines":[4],"attachments":["/a"],"request":{"token":"t","item":9,"kind":"question","options":["a","b"],"typed":true,"outcome":"","job":"j"},"read":false,"future":{}}"#
        guard case .chatEntry(let e)? = d(#"{"kind":"chat.entry","entry":"# + entry + "}") else { return XCTFail("chat.entry") }
        XCTAssertEqual(e.id, "e1")
        XCTAssertEqual(e.at, 1700)
        XCTAssertEqual(e.who, ChatWho(kind: "session", name: "web", label: "Web", yours: true, status: "waiting", color: 3))
        XCTAssertEqual(e.request, ChatRequest(token: "t", item: 9, kind: "question", options: ["a", "b"], typed: true, job: "j"))
        XCTAssertEqual(e.lines, [4])

        guard case .chatNeeds(let n)? = d(#"{"kind":"chat.needs","entries":["# + entry + "]}") else { return XCTFail("chat.needs") }
        XCTAssertEqual(n, [e])
        XCTAssertEqual(d(#"{"kind":"chat.needs","entries":[]}"#), .chatNeeds([]))

        XCTAssertEqual(d(#"{"kind":"chat.badge","needs":1,"unread":4}"#), .chatBadge(ChatBadge(needs: 1, unread: 4)))
        let who = ChatWho(kind: "pixie")
        XCTAssertEqual(d(#"{"kind":"chat.badge","needs":0,"unread":1,"preview":{"id":"p","who":{"kind":"pixie"},"text":"yo"}}"#),
                       .chatBadge(ChatBadge(needs: 0, unread: 1, preview: .init(id: "p", who: who, text: "yo"))))
        XCTAssertEqual(d(#"{"kind":"chat.reset"}"#), .chatReset)
        XCTAssertNil(d(#"{"kind":"chat.entry"}"#), "an entry event without its entry is dropped")
    }

    func testDecodesChatLive() {
        XCTAssertEqual(d(#"{"kind":"chat.live","who":"you","text":"what time"}"#), .chatLive(ChatLive(who: "you", text: "what time")))
        XCTAssertEqual(d(#"{"kind":"chat.live","who":"coordinator","text":"Looking.","job":"j1","extra":1}"#),
                       .chatLive(ChatLive(who: "coordinator", text: "Looking.", job: "j1")))
        XCTAssertEqual(d(#"{"kind":"chat.live","who":"pixie","text":""}"#), .chatLive(ChatLive(who: "pixie", text: "")))
        XCTAssertEqual(d(#"{"kind":"chat.live","who":"pixie"}"#), .chatLive(ChatLive(who: "pixie", text: "")), "no text clears")
        XCTAssertNil(d(#"{"kind":"chat.live","text":"x"}"#), "a live line without its who is dropped")
    }
}
