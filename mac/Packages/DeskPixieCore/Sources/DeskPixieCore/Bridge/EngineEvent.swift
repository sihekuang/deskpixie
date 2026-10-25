import Foundation

/// What `dp_configure` gets: only paths (P14a). Everything else (model, voice, pack, keys…)
/// the core reads from settings.json and env/.env itself.
public struct EngineConfig: Codable, Sendable, Equatable {
    /// `~/Library/Application Support/com.sihekuang.deskpixie` (settings.json, pixies/, canvas/…).
    public var appSupport: String
    /// The app's bundled packs (`Contents/Resources/pixies`).
    public var bundledPixies: String
    /// The bundled canvas design system (`Contents/Resources/design-system`).
    public var designSystem: String
    /// `Contents/Resources/DeepFilterNet3.tar.gz`, or "" when the build has no libdf.
    public var dfn3ModelPath: String
    public var platform: String
    /// Transitional: the Phase 3 core resolves packs from this; P14a's core ignores it (Go's
    /// JSON decoding drops unknown keys). Drop once P14a has landed.
    public var packRoots: [String]?

    public init(appSupport: String, bundledPixies: String, designSystem: String, dfn3ModelPath: String,
                platform: String = "macos", packRoots: [String]? = nil) {
        self.appSupport = appSupport
        self.bundledPixies = bundledPixies
        self.designSystem = designSystem
        self.dfn3ModelPath = dfn3ModelPath
        self.platform = platform
        self.packRoots = packRoots
    }

    /// The payload for an app whose resources are at `resources`. `exists` decides whether the
    /// optional DFN3 model was bundled.
    public static func forBundle(resources: URL, appSupport: URL,
                                 exists: (String) -> Bool = { FileManager.default.fileExists(atPath: $0) })
        -> EngineConfig {
        let pixies = resources.appendingPathComponent("pixies", isDirectory: true).path
        let model = resources.appendingPathComponent("DeepFilterNet3.tar.gz").path
        return EngineConfig(appSupport: appSupport.path,
                            bundledPixies: pixies,
                            designSystem: resources.appendingPathComponent("design-system", isDirectory: true).path,
                            dfn3ModelPath: exists(model) ? model : "",
                            packRoots: [pixies, appSupport.appendingPathComponent("pixies", isDirectory: true).path])
    }
}

/// Payload of the `pack` event: the active pack changed (or its scale did).
public struct PackEvent: Decodable, Sendable, Equatable {
    public let pack: Pack
    public let sheetPath: String
    public let scale: Int
    /// Union hit mask: one bit per pixel of a cell (w×h, row-major), base64 on the wire.
    public let hitMask: Data?
    /// The sheet file's modification time: a sheet redrawn at the same path is read again
    /// when it changes (#266).
    public let sheetVersion: Int64?

    public init(pack: Pack, sheetPath: String, scale: Int, hitMask: Data?, sheetVersion: Int64? = nil) {
        self.pack = pack
        self.sheetPath = sheetPath
        self.scale = scale
        self.hitMask = hitMask
        self.sheetVersion = sheetVersion
    }

    /// Whether the sheet must be read again rather than reusing the one at `path` read at
    /// `version`.
    public func needsSheet(currentPath: String?, currentVersion: Int64?) -> Bool {
        sheetPath != currentPath || sheetVersion != currentVersion
    }
}

/// UI events produced by the Go core (`dp_poll_event`). See the plan's "Shared contract".
public enum EngineEvent: Sendable, Equatable {
    case state(String)
    case mouth(Int)
    case status(String)
    case transcript(role: String, text: String)
    /// The user's words so far while they talk (#251), for the live caption.
    case heard(String)
    /// Pixie's words so far while it speaks (#253), for the caption.
    case saying(String)
    /// The sessions Pixie is about to speak of (#261), for the caption's buttons.
    case about([String])
    case error(String)
    case mic(start: Bool)
    case metrics(String)
    /// Re-read `app.state` and rebuild the tray menu.
    case tray
    case pack(PackEvent)
    /// Move the pixie panel (logical top-left); both nil = the default spot.
    case position(x: Double?, y: Double?)
    case hotkey(combo: String)
    /// Audio device UIDs to pin for the next start (nil = system default).
    case devices(input: String?, output: String?)
    /// `update`: the bubble line the notification is about (#199); a click on the notification
    /// does what a click on the line does.
    case notify(title: String, body: String, sound: String?, update: UpdateLine? = nil)
    case canvasOpen(url: String, title: String)
    case settingsChanged
    /// Pixie's open_settings (#288): show the Settings window, at the setting when there is one.
    case settingsOpen(SettingsHit?)
    case restart
    /// The unheard agent updates, oldest first (the whole current list; empty = none left).
    case updates([UpdateLine])
    /// The roster beside Pixie (#91): Pixie's agents and your pix sessions, with counts.
    case agents(RosterView)
    /// Bring up the pane of an agent waiting at a prompt in its terminal (#110).
    case showPane(PaneRequest)
    /// Show a terminal by walking a core-built fallback plan (#116).
    case focusPlan(FocusPlan)
    /// A spoken line for a message typed in the text box (#221).
    case chatLine(ChatLine)
    /// An update card: sessions' finished work, one card for rows that came together (#286).
    case replyCard(ReplyCard)
    /// The hotkey in text mode: open the text box and focus it.
    case chatFocus
    /// replyMode changed: "voice" or "text".
    case replyMode(String)
    /// alwaysShowCaption, captionDismissSecs, captionFadeSecs or replyHistoryMins changed (#224,
    /// #254, #263).
    case captions(always: Bool, dismissSecs: Int, fadeSecs: Int, historyMins: Int)
    /// The attachments waiting for the next request (#240), the whole set.
    case attachments([AttachmentItem])
    /// Pixie's take_clipboard: read the clipboard, add what's there, then call clipboard.done.
    case clipboardRead(request: String)
    /// An entry in the chat thread was added or changed (#302).
    case chatEntry(ChatEntry)
    /// The open requests in the chat thread, the whole set (#302).
    case chatNeeds([ChatEntry])
    /// What Pixie's badge shows, with the newest entry that came (#302).
    case chatBadge(ChatBadge)
    /// The thread's entries went (cleared, or aged out): load it again (#302).
    case chatReset

    private struct Kind: Decodable { var kind: String }

    private struct Voice: Decodable {
        var state: String?
        var level: Int?
        var text: String?
        var role: String?
        var sessions: [String]?
    }

    private struct Position: Decodable { var x: Double?, y: Double? }
    private struct Hotkey: Decodable { var combo: String }
    private struct Devices: Decodable { var input: String?, output: String? }
    private struct Notify: Decodable { var title: String, body: String, sound: String?, update: UpdateLine? }
    private struct CanvasOpen: Decodable { var url: String, title: String }
    private struct Updates: Decodable { var lines: [UpdateLine] }
    private struct Agents: Decodable { var roster: RosterView }
    private struct FocusPlanEvent: Decodable { var plan: FocusPlan }
    private struct ReplyModeEvent: Decodable { var mode: String }
    private struct CaptionsEvent: Decodable { var always: Bool; var dismissSecs: Int; var fadeSecs: Int?; var historyMins: Int? }
    private struct AttachmentsEvent: Decodable { var items: [AttachmentItem] }
    private struct ChatEntryEvent: Decodable { var entry: ChatEntry }
    private struct ChatNeedsEvent: Decodable { var entries: [ChatEntry] }
    private struct ClipboardRead: Decodable { var request: String }

    /// Decodes one event JSON object; unknown kinds, malformed JSON and events missing a
    /// required field yield nil.
    public static func decode(_ json: Data) -> EngineEvent? {
        let dec = JSONDecoder()
        guard let k = try? dec.decode(Kind.self, from: json) else { return nil }
        func get<T: Decodable>(_: T.Type) -> T? { try? dec.decode(T.self, from: json) }
        switch k.kind {
        case "state", "mouth", "status", "transcript", "heard", "saying", "about", "error", "mic", "metrics":
            guard let w = get(Voice.self) else { return nil }
            switch k.kind {
            case "state": return .state(w.state ?? "")
            case "mouth": return .mouth(w.level ?? 0)
            case "status": return .status(w.text ?? "")
            case "transcript": return .transcript(role: w.role ?? "", text: w.text ?? "")
            case "heard": return .heard(w.text ?? "")
            case "saying": return .saying(w.text ?? "")
            case "about": return .about(w.sessions ?? [])
            case "error": return .error(w.text ?? "")
            case "mic": return .mic(start: w.text == "start")
            default: return .metrics(w.text ?? "")
            }
        case "tray": return .tray
        case "settings.changed": return .settingsChanged
        case "settings.open": return .settingsOpen(get(SettingsHit.self))
        case "restart": return .restart
        case "pack": return get(PackEvent.self).map { .pack($0) }
        case "position": return get(Position.self).map { .position(x: $0.x, y: $0.y) }
        case "hotkey": return get(Hotkey.self).map { .hotkey(combo: $0.combo) }
        case "devices": return get(Devices.self).map { .devices(input: $0.input, output: $0.output) }
        case "notify":
            return get(Notify.self).map { .notify(title: $0.title, body: $0.body, sound: $0.sound, update: $0.update) }
        case "canvas.open": return get(CanvasOpen.self).map { .canvasOpen(url: $0.url, title: $0.title) }
        case "updates": return get(Updates.self).map { .updates($0.lines) }
        case "agents": return get(Agents.self).map { .agents($0.roster) }
        case "showPane": return get(PaneRequest.self).map { .showPane($0) }
        case "focusPlan": return get(FocusPlanEvent.self).map { .focusPlan($0.plan) }
        case "chat.line": return get(ChatLine.self).map { .chatLine($0) }
        case "reply.card": return get(ReplyCard.self).map { .replyCard($0) }
        case "chat.focus": return .chatFocus
        case "replyMode": return get(ReplyModeEvent.self).map { .replyMode($0.mode) }
        case "captions": return get(CaptionsEvent.self).map { .captions(always: $0.always, dismissSecs: $0.dismissSecs, fadeSecs: $0.fadeSecs ?? 4, historyMins: $0.historyMins ?? 5) }
        case "attachments": return get(AttachmentsEvent.self).map { .attachments($0.items) }
        case "clipboard.read": return get(ClipboardRead.self).map { .clipboardRead(request: $0.request) }
        case "chat.entry": return get(ChatEntryEvent.self).map { .chatEntry($0.entry) }
        case "chat.needs": return get(ChatNeedsEvent.self).map { .chatNeeds($0.entries) }
        case "chat.badge": return get(ChatBadge.self).map { .chatBadge($0) }
        case "chat.reset": return .chatReset
        default: return nil
        }
    }
}
