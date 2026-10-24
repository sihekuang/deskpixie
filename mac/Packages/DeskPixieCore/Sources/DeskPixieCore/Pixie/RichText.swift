import AppKit
import Foundation

/// One piece of a message in Pixie's bubbles, as the core's `text.spans` splits it (#277):
/// plain text, a link, a file or folder that exists, code, a commit or an email address. `raw`
/// is the source it came from; `text` is what is shown.
public struct RichSpan: Codable, Sendable, Equatable {
    public let kind: String
    public let text: String
    public let raw: String
    public let bold: Bool
    public let url: String?
    /// A file or folder, absolute; `line` and `col` are where in it (0 = none).
    public let path: String?
    public let dir: Bool
    /// Opening it would launch or run something (an app, a script): a click only shows it.
    public let reveal: Bool
    public let line: Int
    public let col: Int
    /// How a path opens: addresses to try in order, the first there is an app for wins.
    public let open: [String]

    public init(kind: String, text: String, raw: String, bold: Bool, url: String?, path: String?, dir: Bool,
                reveal: Bool = false, line: Int, col: Int, open: [String]) {
        self.kind = kind
        self.text = text
        self.raw = raw
        self.bold = bold
        self.url = url
        self.path = path
        self.dir = dir
        self.reveal = reveal
        self.line = line
        self.col = col
        self.open = open
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(kind: try c.decode(String.self, forKey: .kind), text: try c.decode(String.self, forKey: .text),
                  raw: try c.decode(String.self, forKey: .raw),
                  bold: try c.decodeIfPresent(Bool.self, forKey: .bold) ?? false,
                  url: try c.decodeIfPresent(String.self, forKey: .url),
                  path: try c.decodeIfPresent(String.self, forKey: .path),
                  dir: try c.decodeIfPresent(Bool.self, forKey: .dir) ?? false,
                  reveal: try c.decodeIfPresent(Bool.self, forKey: .reveal) ?? false,
                  line: try c.decodeIfPresent(Int.self, forKey: .line) ?? 0,
                  col: try c.decodeIfPresent(Int.self, forKey: .col) ?? 0,
                  open: try c.decodeIfPresent([String].self, forKey: .open) ?? [])
    }

    private enum CodingKeys: String, CodingKey { case kind, text, raw, bold, url, path, dir, reveal, line, col, open }

    /// Plain text: what shows until the core's spans come, or when they can't.
    public static func plain(_ text: String) -> RichSpan {
        RichSpan(kind: "text", text: text, raw: text, bold: false, url: nil, path: nil, dir: false, line: 0, col: 0,
                 open: [])
    }

    /// It does something when clicked.
    public var isActionable: Bool { action != nil }

    /// A click opens (in order, the first that works) or copies.
    public var action: RichAction? {
        switch kind {
        case "link":
            guard let url, let u = URL(string: url) else { return nil }
            return .open([u])
        case "path":
            if reveal, let path { return .reveal(path) }
            let urls = open.compactMap(URL.init(string:))
            return urls.isEmpty ? nil : .open(urls)
        case "code", "commit", "email":
            return .copy(text)
        default:
            return nil
        }
    }

    /// What a hover says.
    public func tooltip(copied: Bool) -> String {
        switch kind {
        case "link":
            return "Open link\n\(url ?? "")"
        case "path":
            if reveal { return "Show in Finder" }
            if dir { return "Open folder" }
            let at = line > 0 && open.count > 1 ? " · line \(line)" : ""
            let what = open.count > 1 ? "Open in your editor" : "Open file"
            return "\(what)\(at)\n⌥-click: show in Finder"
        case "code", "commit", "email":
            if copied { return "Copied" }
            return ["code": "Copy", "commit": "Copy commit", "email": "Copy email"][kind] ?? "Copy"
        default:
            return ""
        }
    }
}

/// What a click on a span does.
public enum RichAction: Equatable, Sendable {
    case open([URL])
    /// Shows the file or folder in the file manager.
    case reveal(String)
    case copy(String)
}

/// The core's `text.spans` answer.
public struct RichSpans: Decodable, Sendable {
    public let spans: [RichSpan]
}

public enum RichText {
    /// The attribute that marks an actionable span: its index in the spans.
    public static let spanKey = NSAttributedString.Key("DeskPixieRichSpan")

    /// The spans shown so far while the reply card types `shown` out: the raw text's words as
    /// far as they are typed. A span half typed is plain text until it is whole.
    public static func prefix(_ spans: [RichSpan], shown: String) -> [RichSpan] {
        var left = shown.filter { !$0.isWhitespace }.count
        var out: [RichSpan] = []
        for s in spans {
            let n = s.raw.filter { !$0.isWhitespace }.count
            if n <= left {
                out.append(s)
                left -= n
                continue
            }
            // The markers before the shown text (`**`, `[`, a backtick) are typed first.
            var lead = 0
            if let r = s.raw.range(of: s.text) {
                lead = s.raw[..<r.lowerBound].filter { !$0.isWhitespace }.count
            }
            let part = upTo(s.text, nonWhitespace: left - lead)
            if !part.isEmpty {
                out.append(RichSpan(kind: "text", text: part, raw: part, bold: s.bold, url: nil, path: nil, dir: false,
                                    line: 0, col: 0, open: []))
            }
            break
        }
        return out
    }

    /// The start of s that holds n characters that aren't whitespace.
    private static func upTo(_ s: String, nonWhitespace n: Int) -> String {
        guard n > 0 else { return "" }
        var seen = 0
        var end = s.startIndex
        for i in s.indices where !s[i].isWhitespace {
            seen += 1
            if seen == n {
                end = s.index(after: i)
                break
            }
        }
        return seen < n ? s : String(s[..<end])
    }

    /// The spans drawn: links in the link colour, paths monospaced with a dotted underline,
    /// code, commits and emails as monospaced chips. Each actionable span carries `spanKey` (the
    /// view shows its tooltip: AppKit's own show only in the active app).
    public static func attributed(_ spans: [RichSpan], font: NSFont, color: NSColor) -> NSAttributedString {
        let out = NSMutableAttributedString()
        let bold = NSFontManager.shared.convert(font, toHaveTrait: .boldFontMask)
        let mono = NSFont.monospacedSystemFont(ofSize: font.pointSize - 1, weight: .regular)
        for (i, s) in spans.enumerated() {
            var a: [NSAttributedString.Key: Any] = [.font: s.bold ? bold : font, .foregroundColor: color]
            if s.isActionable {
                a[spanKey] = i
                switch s.kind {
                case "link":
                    a[.foregroundColor] = NSColor.linkColor
                    a[.underlineStyle] = NSUnderlineStyle.single.rawValue
                    a[.underlineColor] = NSColor.linkColor.withAlphaComponent(0.4)
                case "path":
                    a[.font] = mono
                    a[.underlineStyle] = NSUnderlineStyle.single.union(.patternDot).rawValue
                    a[.underlineColor] = NSColor.secondaryLabelColor
                default:
                    a[.font] = mono
                    a[.backgroundColor] = NSColor.labelColor.withAlphaComponent(0.1)
                }
            } else if s.kind == "code" {
                a[.font] = mono
            }
            out.append(NSAttributedString(string: s.text, attributes: a))
        }
        return out
    }
}

/// The spans of the messages shown, fetched from the core once per text and sessions (#277).
/// The call runs off the main thread; until it answers, `spans` is nil and the view draws the
/// plain text. A failed call leaves the text plain.
@MainActor
public final class RichTextStore: ObservableObject {
    public typealias Fetch = @Sendable (_ text: String, _ sessions: [String]) throws -> [RichSpan]

    private struct Key: Hashable {
        let text: String
        let sessions: [String]
    }

    private let fetch: Fetch
    private let queue: DispatchQueue
    private var cache: [Key: [RichSpan]] = [:]
    private var order: [Key] = []
    private var pending: Set<Key> = []
    /// Spans kept from before a reset: shown until they are fetched again.
    private var stale: Set<Key> = []
    /// Bumped when spans arrive, so the views showing them draw again.
    @Published public private(set) var version = 0
    /// Called after spans arrive: the text may have changed size, so the panel lays out again.
    public var onChange: (() -> Void)?
    /// The most messages kept.
    static let limit = 128

    public init(queue: DispatchQueue = DispatchQueue(label: "deskpixie.richtext"), fetch: @escaping Fetch) {
        self.fetch = fetch
        self.queue = queue
    }

    /// The store calling the core's `text.spans`.
    public convenience init(engine: CoreEngine) {
        self.init { text, sessions in
            struct Args: Encodable { let text: String; let sessions: [String] }
            return try engine.call("text.spans", Args(text: text, sessions: sessions), as: RichSpans.self).spans
        }
    }

    /// Every message's spans are fetched again (a setting changed where files open); the old
    /// ones show meanwhile.
    public func reset() {
        stale = Set(cache.keys)
        version += 1
    }

    /// The spans of text, or nil while they are on their way (asked for on first use).
    public func spans(_ text: String, sessions: [String]) -> [RichSpan]? {
        let key = Key(text: text, sessions: sessions)
        if let have = cache[key], !stale.contains(key) { return have }
        guard !text.isEmpty else { return [] }
        stale.remove(key)
        if pending.insert(key).inserted {
            let fetch = fetch
            queue.async { [weak self] in
                let spans = (try? fetch(text, sessions)) ?? [RichSpan.plain(text)]
                Task { @MainActor in self?.arrived(key, spans) }
            }
        }
        return cache[key]
    }

    private func arrived(_ key: Key, _ spans: [RichSpan]) {
        pending.remove(key)
        if cache.updateValue(spans, forKey: key) == nil { order.append(key) }
        if order.count > Self.limit {
            let old = order.removeFirst()
            cache[old] = nil
            stale.remove(old)
        }
        version += 1
        onChange?()
    }
}

/// A click on a span is the span's alone. SwiftUI's gestures see clicks on views inside them, so
/// a bubble row's button would fire too: it asks `takeSpanPress` first and skips the click.
@MainActor
public enum RichTextClicks {
    private static var spanPressAt: TimeInterval?

    public static func spanPressed(at time: TimeInterval) { spanPressAt = time }

    /// True, once, when a span took the press of the click being handled.
    public static func takeSpanPress(now: TimeInterval = ProcessInfo.processInfo.systemUptime) -> Bool {
        defer { spanPressAt = nil }
        guard let at = spanPressAt else { return false }
        return now - at < 1
    }
}

/// The OS actions a span's click needs; the real one is `SystemRichTextPrimitives`.
public protocol RichTextPrimitives: Sendable {
    /// There is an app that opens url.
    func canOpen(_ url: URL) -> Bool
    func open(_ url: URL) -> Bool
    /// Shows the file or folder in the file manager.
    func reveal(_ path: String)
    func copy(_ text: String)
}

/// Carries out a click on a span (#277): the core decided what each span opens; this tries its
/// addresses in order, the first there is an app for wins.
public struct RichTextActions: Sendable {
    public enum Outcome: Equatable, Sendable { case opened, revealed, copied, failed, none }

    let os: RichTextPrimitives

    public init(os: RichTextPrimitives) { self.os = os }

    public func perform(_ span: RichSpan, reveal: Bool) -> Outcome {
        switch span.action {
        case .copy(let text):
            os.copy(text)
            return .copied
        case .reveal(let path):
            os.reveal(path)
            return .revealed
        case .open(let urls):
            if reveal, span.kind == "path", let path = span.path {
                os.reveal(path)
                return .revealed
            }
            for url in urls where os.canOpen(url) {
                if os.open(url) { return .opened }
            }
            return .failed
        case nil:
            return .none
        }
    }
}
