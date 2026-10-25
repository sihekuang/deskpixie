import Foundation

/// Who says a chat entry (#302), as the core's `chat.*` calls and events send it. `name` is a
/// session's address, never shown; `label` is what to show.
public struct ChatWho: Codable, Equatable, Sendable {
    /// you | pixie | coordinator | session
    public var kind: String
    public var name: String
    public var label: String
    public var yours: Bool
    public var status: String
    /// 0..5, the same for a name every time.
    public var color: Int

    public init(kind: String, name: String = "", label: String = "", yours: Bool = false,
                status: String = "", color: Int = 0) {
        self.kind = kind
        self.name = name
        self.label = label
        self.yours = yours
        self.status = status
        self.color = color
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(kind: try c.decode(String.self, forKey: .kind),
                  name: try c.decodeIfPresent(String.self, forKey: .name) ?? "",
                  label: try c.decodeIfPresent(String.self, forKey: .label) ?? "",
                  yours: try c.decodeIfPresent(Bool.self, forKey: .yours) ?? false,
                  status: try c.decodeIfPresent(String.self, forKey: .status) ?? "",
                  color: try c.decodeIfPresent(Int.self, forKey: .color) ?? 0)
    }

    private enum CodingKeys: String, CodingKey { case kind, name, label, yours, status, color }
}

/// A question or permission an entry asks of you; open until it has an `outcome`.
public struct ChatRequest: Codable, Equatable, Sendable {
    public var token: String
    public var item: UInt64
    /// permission | question
    public var kind: String
    public var options: [String]
    public var typed: Bool
    public var outcome: String
    /// The job asking ("" = a session's question, or a permission no job owns).
    public var job: String

    public init(token: String = "", item: UInt64, kind: String, options: [String] = [],
                typed: Bool = false, outcome: String = "", job: String = "") {
        self.token = token
        self.item = item
        self.kind = kind
        self.options = options
        self.typed = typed
        self.outcome = outcome
        self.job = job
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(token: try c.decodeIfPresent(String.self, forKey: .token) ?? "",
                  item: try c.decode(UInt64.self, forKey: .item),
                  kind: try c.decode(String.self, forKey: .kind),
                  options: try c.decodeIfPresent([String].self, forKey: .options) ?? [],
                  typed: try c.decodeIfPresent(Bool.self, forKey: .typed) ?? false,
                  outcome: try c.decodeIfPresent(String.self, forKey: .outcome) ?? "",
                  job: try c.decodeIfPresent(String.self, forKey: .job) ?? "")
    }

    private enum CodingKeys: String, CodingKey { case token, item, kind, options, typed, outcome, job }
}

/// One message in the thread. The `id` is opaque (never parse it); an entry with an ID already
/// shown replaces it. `sessions` holds session addresses, not labels.
public struct ChatEntry: Codable, Equatable, Sendable, Identifiable {
    public var id: String
    /// Milliseconds since the epoch.
    public var at: Int64
    public var who: ChatWho
    public var text: String
    public var spoken: Bool
    public var sessions: [String]
    public var lines: [UInt64]
    public var attachments: [String]
    public var request: ChatRequest?
    public var read: Bool

    public init(id: String, at: Int64 = 0, who: ChatWho, text: String, spoken: Bool = false,
                sessions: [String] = [], lines: [UInt64] = [], attachments: [String] = [],
                request: ChatRequest? = nil, read: Bool = false) {
        self.id = id
        self.at = at
        self.who = who
        self.text = text
        self.spoken = spoken
        self.sessions = sessions
        self.lines = lines
        self.attachments = attachments
        self.request = request
        self.read = read
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(id: try c.decode(String.self, forKey: .id),
                  at: try c.decode(Int64.self, forKey: .at),
                  who: try c.decode(ChatWho.self, forKey: .who),
                  text: try c.decode(String.self, forKey: .text),
                  spoken: try c.decodeIfPresent(Bool.self, forKey: .spoken) ?? false,
                  sessions: try c.decodeIfPresent([String].self, forKey: .sessions) ?? [],
                  lines: try c.decodeIfPresent([UInt64].self, forKey: .lines) ?? [],
                  attachments: try c.decodeIfPresent([String].self, forKey: .attachments) ?? [],
                  request: try c.decodeIfPresent(ChatRequest.self, forKey: .request),
                  read: try c.decodeIfPresent(Bool.self, forKey: .read) ?? false)
    }

    private enum CodingKeys: String, CodingKey {
        case id, at, who, text, spoken, sessions, lines, attachments, request, read
    }
}

/// What Pixie's badge shows (`chat.badge`): requests waiting on you and entries unread, plus,
/// on the event, the newest entry that came.
public struct ChatBadge: Codable, Equatable, Sendable {
    public struct Preview: Codable, Equatable, Sendable {
        public var id: String
        public var who: ChatWho
        public var text: String

        public init(id: String, who: ChatWho, text: String) {
            self.id = id
            self.who = who
            self.text = text
        }
    }

    public var needs: Int
    public var unread: Int
    public var preview: Preview?

    public init(needs: Int = 0, unread: Int = 0, preview: Preview? = nil) {
        self.needs = needs
        self.unread = unread
        self.preview = preview
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(needs: try c.decodeIfPresent(Int.self, forKey: .needs) ?? 0,
                  unread: try c.decodeIfPresent(Int.self, forKey: .unread) ?? 0,
                  preview: try c.decodeIfPresent(Preview.self, forKey: .preview))
    }

    private enum CodingKeys: String, CodingKey { case needs, unread, preview }
}

/// The `chat.*` dp_calls the chat window uses (Go `core/app/thread.go`). Blocking: callers run
/// them off the main thread.
public protocol ChatClient: Sendable {
    /// `chat.entries {before, limit}`: up to `limit` entries before the entry `before` (nil =
    /// from the end), newest first.
    func entries(before: String?, limit: Int) throws -> [ChatEntry]
    /// `chat.needs`: the open requests, oldest first.
    func needs() throws -> [ChatEntry]
    /// `chat.read {upTo}`: marks every entry up to and including this one read.
    func read(upTo: String) throws
    /// Sends a message typed in the box; `session` is a session's address when it replies to one.
    func send(_ text: String, attachments: [String], session: String?) throws
    func decide(token: String, allow: Bool) throws
    /// `option` counts from 1.
    func choose(token: String, option: Int) throws
    func typedAnswer(token: String, text: String) throws
    /// Stops what Pixie is working on.
    func stop() throws
}
