import Foundation

/// Someone a message typed in the chat window can go to on its own (#302), as the core's
/// `chat.recipients` call and event list them: a session of yours that is running, or one of
/// Pixie's workers. `name` is its address and the @name that mentions it.
public struct ChatRecipient: Codable, Equatable, Sendable, Identifiable {
    public var name: String
    public var label: String
    /// session | worker
    public var kind: String
    public var yours: Bool
    public var status: String
    public var color: Int

    public var id: String { name }

    public init(name: String, label: String = "", kind: String = "session", yours: Bool = true,
                status: String = "", color: Int = 0) {
        self.name = name
        self.label = label.isEmpty ? name : label
        self.kind = kind
        self.yours = yours
        self.status = status
        self.color = color
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(name: try c.decode(String.self, forKey: .name),
                  label: try c.decodeIfPresent(String.self, forKey: .label) ?? "",
                  kind: try c.decodeIfPresent(String.self, forKey: .kind) ?? "session",
                  yours: try c.decodeIfPresent(Bool.self, forKey: .yours) ?? false,
                  status: try c.decodeIfPresent(String.self, forKey: .status) ?? "",
                  color: try c.decodeIfPresent(Int.self, forKey: .color) ?? 0)
    }

    private enum CodingKeys: String, CodingKey { case name, label, kind, yours, status, color }

    /// As the thread names who said something, for the reply chip.
    public var who: ChatWho {
        ChatWho(kind: "session", name: name, label: label, yours: yours, status: status, color: color)
    }
}

/// One message, one recipient (#302): the core's rule (`core/chat/recipients.go`), run here too
/// so the box says so before you send. The core stays the authority.
public enum ChatRecipients {
    /// A rune an @name can hold.
    private static func isHandle(_ c: Character) -> Bool {
        c.isLetter || c.isNumber || c == "-" || c == "_" || c == "."
    }

    /// A recipient the text names, and the word after the @ as typed.
    public struct Mention: Equatable, Sendable {
        public let recipient: ChatRecipient
        public let said: String
    }

    /// The recipients named with @name, in order, each once. The name is the address or the
    /// label (a label of one word: "@beta"); an address is matched first, so a label never
    /// shadows another recipient's address, and with a `target` (its address) a label it shares
    /// with others names the target. An @ at the start or after a space, and the whole word, in
    /// any case ("a@b", an email address, "@docs2" for docs name no one). A full stop after it
    /// ends the sentence, not the name. The core's `chat.Mentions` is the same.
    public static func mentions(in text: String, _ recipients: [ChatRecipient],
                                target: String? = nil) -> [Mention] {
        var out: [Mention] = []
        let chars = Array(text)
        var i = 0
        while i < chars.count {
            defer { i += 1 }
            guard chars[i] == "@", i == 0 || chars[i - 1].isWhitespace else { continue }
            var j = i + 1
            while j < chars.count, isHandle(chars[j]) { j += 1 }
            var name = String(chars[(i + 1)..<j])
            while name.hasSuffix(".") { name.removeLast() }
            i = j - 1
            guard !name.isEmpty else { continue }
            if let r = named(name, recipients, target: target),
               !out.contains(where: { $0.recipient.name == r.name }) {
                out.append(Mention(recipient: r, said: name))
            }
        }
        return out
    }

    /// The recipient an @name is: the one with that address, else the target if it is its
    /// label, else the first with that label.
    private static func named(_ name: String, _ recipients: [ChatRecipient], target: String?) -> ChatRecipient? {
        let lower = name.lowercased()
        if let r = recipients.first(where: { $0.name.lowercased() == lower }) { return r }
        if let target, !target.isEmpty,
           let r = recipients.first(where: { $0.name == target && $0.label.lowercased() == lower }) { return r }
        return recipients.first { $0.label.lowercased() == lower }
    }

    /// Why this can't go as it is, nil when it can. `target` is the one chosen (nil: Pixie),
    /// `label` names it; `knowsRecipients`: the list has come, so a target missing from it has
    /// ended (until then the core decides).
    public static func check(_ text: String, target: String?, label: String,
                             recipients: [ChatRecipient], knowsRecipients: Bool = true) -> String? {
        let named = mentions(in: text, recipients, target: target)
        guard let target else {
            if named.count >= 2 { return "Send to one at a time: pick @\(named[0].said) or @\(named[1].said)." }
            if let one = named.first { return "Pick @\(one.said) from the list to send it to them." }
            return nil
        }
        guard let to = recipients.first(where: { !target.isEmpty && $0.name == target }) else {
            guard knowsRecipients else { return nil }
            let name = !label.isEmpty ? label : !target.isEmpty ? target : "That session"
            return "\(name) isn't running anymore."
        }
        if let other = named.first(where: { $0.recipient.name != to.name }) {
            return "This goes to \(to.label) only. Remove @\(other.said), or pick them instead."
        }
        return nil
    }
}

/// A row of the @ list: Pixie (the default) or a recipient.
public enum MentionCandidate: Equatable, Sendable, Identifiable {
    case pixie
    case recipient(ChatRecipient)

    public var id: String {
        if case .recipient(let r) = self { return "r:" + r.name }
        return "pixie"
    }

    public var label: String {
        if case .recipient(let r) = self { return r.label }
        return "Pixie"
    }

    /// Under the name: Pixie's "Message Pixie", else the @name.
    public var detail: String {
        if case .recipient(let r) = self { return "@" + r.name }
        return "Message Pixie"
    }
}

/// The @ being typed in the box (#302): where it starts and what follows it up to the caret.
/// Picking from its list sets the one target and takes the @… out of the text.
public struct MentionQuery: Equatable, Sendable {
    /// Where the @ is, in characters.
    public let start: Int
    /// What follows it up to the caret.
    public let query: String

    public init(start: Int, query: String) {
        self.start = start
        self.query = query
    }

    /// The @ word the caret is in: an @ at the start or after a space, then no space up to the
    /// caret (`caret` in characters, clamped). Nil when there's none: a space closes the list.
    public static func active(in text: String, caret: Int) -> MentionQuery? {
        let chars = Array(text)
        let end = max(0, min(caret, chars.count))
        var i = end - 1
        while i >= 0, !chars[i].isWhitespace, chars[i] != "@" { i -= 1 }
        guard i >= 0, chars[i] == "@", i == 0 || chars[i - 1].isWhitespace else { return nil }
        return MentionQuery(start: i, query: String(chars[(i + 1)..<end]))
    }

    /// Pixie, then the recipients, that fit what's typed: those whose name or @name starts with
    /// it first, then those that contain it, in any case; each kept in list order. A full stop
    /// at the end is left out, as `ChatRecipients.mentions` leaves it out.
    public func candidates(_ recipients: [ChatRecipient]) -> [MentionCandidate] {
        let all: [MentionCandidate] = [.pixie] + recipients.map { .recipient($0) }
        var q = query.lowercased()
        while q.hasSuffix(".") { q.removeLast() }
        guard !q.isEmpty else { return all }
        func keys(_ c: MentionCandidate) -> [String] {
            if case .recipient(let r) = c { return [r.label.lowercased(), r.name.lowercased()] }
            return ["pixie"]
        }
        let prefix = all.filter { keys($0).contains { $0.hasPrefix(q) } }
        let contains = all.filter { c in !prefix.contains(c) && keys(c).contains { $0.contains(q) } }
        return prefix + contains
    }

    /// The row Return or Tab picks (`row`, clamped), nil when nothing fits: then Return sends
    /// the text as it is, @word and all.
    public func selected(row: Int, _ recipients: [ChatRecipient]) -> MentionCandidate? {
        let rows = candidates(recipients)
        return rows.isEmpty ? nil : rows[max(0, min(row, rows.count - 1))]
    }

    /// "No one called <query>" when nothing fits.
    public func emptyLabel() -> String { "No one called \(query)" }

    /// The text after picking: this @… goes, and so does any @name (or @label) of the target it
    /// replaces (`previous`), so the text never names two. The target is the pick's (nil: Pixie).
    public func pick(_ candidate: MentionCandidate, in text: String, caret: Int,
                     previous: ChatRecipient?) -> (text: String, target: ChatRecipient?) {
        var chars = Array(text)
        let end = max(start, min(caret, chars.count))
        chars.removeSubrange(start..<end)
        var out = String(chars)
        let target: ChatRecipient? = if case .recipient(let r) = candidate { r } else { nil }
        if let previous, previous.name != target?.name {
            out = Self.removingMentions(of: previous, from: out)
        }
        return (out, target)
    }

    /// The text without @name (whole words, any case), each with one space beside it.
    static func removingMentions(of target: ChatRecipient, from text: String) -> String {
        let one = target
        var chars = Array(text)
        var i = 0
        while i < chars.count {
            guard chars[i] == "@", i == 0 || chars[i - 1].isWhitespace else { i += 1; continue }
            var j = i + 1
            while j < chars.count, chars[j].isLetter || chars[j].isNumber || "-_.".contains(chars[j]) { j += 1 }
            let word = String(chars[i..<j])
            guard !ChatRecipients.mentions(in: word, [one]).isEmpty else { i = j; continue }
            var from = i, to = j
            // The full stop after it stays (it ended the sentence).
            while to > i + 1, chars[to - 1] == "." { to -= 1 }
            if to < chars.count, chars[to] == " " { to += 1 } else if from > 0, chars[from - 1] == " " { from -= 1 }
            chars.removeSubrange(from..<to)
            i = from
        }
        return String(chars)
    }
}
