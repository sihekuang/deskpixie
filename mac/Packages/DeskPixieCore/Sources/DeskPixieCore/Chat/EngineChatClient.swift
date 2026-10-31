import Foundation

/// `ChatClient` over the Go core's `dp_call` (`core/app/thread.go`, `chat.go`, `app.go`).
public struct EngineChatClient: ChatClient, Sendable {
    private let engine: CoreEngine

    public init(engine: CoreEngine) { self.engine = engine }

    private struct EntriesArgs: Encodable {
        /// "" = from the end, as Go reads it.
        let before: String
        let limit: Int
    }

    private struct Entries: Decodable { var entries: [ChatEntry] }

    private struct ReadArgs: Encodable { let upTo: String }

    private struct SendArgs: Encodable {
        let text: String
        /// Left out when the message goes to Pixie; an empty one is the core's to refuse.
        let session: String?
        /// The chat window: the core applies its one-recipient rule (the box under Pixie doesn't).
        var from = "window"
    }

    private struct DecideArgs: Encodable {
        let token: String
        let allow: Bool
    }

    private struct ChooseArgs: Encodable {
        let token: String
        let option: Int
    }

    private struct PickArgs: Encodable {
        let token: String
        let options: [Int]
    }

    private struct DismissArgs: Encodable {
        let id: UInt64
    }

    private struct TokenArgs: Encodable {
        let token: String
    }

    private struct TypedArgs: Encodable {
        let token: String
        let text: String
    }

    public func entries(before: String?, limit: Int) throws -> [ChatEntry] {
        try engine.call("chat.entries", EntriesArgs(before: before ?? "", limit: limit), as: Entries.self).entries
    }

    public func needs() throws -> [ChatEntry] {
        try engine.call("chat.needs", as: Entries.self).entries
    }

    public func badge() throws -> ChatBadge {
        try engine.call("chat.badge", as: ChatBadge.self)
    }

    public func clear() throws {
        _ = try engine.call("chat.clear")
    }

    public func read(upTo: String) throws {
        _ = try engine.call("chat.read", ReadArgs(upTo: upTo))
    }

    public func send(_ text: String, session: String?) throws {
        _ = try engine.call("chat.send", SendArgs(text: text, session: session))
    }

    public func decide(token: String, allow: Bool) throws {
        _ = try engine.call("agent.decide", DecideArgs(token: token, allow: allow))
    }

    public func choose(token: String, option: Int) throws {
        _ = try engine.call("agent.choose", ChooseArgs(token: token, option: option))
    }

    public func pick(token: String, options: [Int]) throws {
        _ = try engine.call("agent.pick", PickArgs(token: token, options: options))
    }

    public func dismiss(item: UInt64) throws {
        _ = try engine.call("agent.dismiss", DismissArgs(id: item))
    }

    public func typedAnswer(token: String, text: String) throws {
        _ = try engine.call("agent.typedAnswer", TypedArgs(token: token, text: text))
    }

    public func chatAbout(token: String) throws {
        _ = try engine.call("agent.chatAbout", TokenArgs(token: token))
    }

    private struct Recipients: Decodable { var recipients: [ChatRecipient] }

    public func recipients() throws -> [ChatRecipient] {
        try engine.call("chat.recipients", as: Recipients.self).recipients
    }

    public func stop() throws {
        _ = try engine.call("chat.stop")
    }

    public func interrupt(name: String) throws {
        _ = try engine.call("agent.interrupt", InterruptArgs(name: name, surface: .chat))
    }
}
