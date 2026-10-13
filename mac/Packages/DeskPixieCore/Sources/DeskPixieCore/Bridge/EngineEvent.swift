import Foundation

/// Configuration passed to `dp_configure` as JSON (camelCase keys: apiKey, model, voice,
/// personaPath, idleTimeoutSecs, pack, packRoots, noisyRoom).
public struct EngineConfig: Codable, Sendable, Equatable {
    public var apiKey: String
    public var model: String
    public var voice: String
    public var personaPath: String?
    public var idleTimeoutSecs: Int
    public var pack: String
    public var packRoots: [String]
    public var noisyRoom: Bool

    public init(apiKey: String, model: String, voice: String, personaPath: String?,
                idleTimeoutSecs: Int, pack: String, packRoots: [String], noisyRoom: Bool) {
        self.apiKey = apiKey
        self.model = model
        self.voice = voice
        self.personaPath = personaPath
        self.idleTimeoutSecs = idleTimeoutSecs
        self.pack = pack
        self.packRoots = packRoots
        self.noisyRoom = noisyRoom
    }
}

/// Payload of the `pack` event: the active pack changed (or its scale did).
public struct PackEvent: Decodable, Sendable, Equatable {
    public let pack: Pack
    public let sheetPath: String
    public let scale: Int
    /// Union hit mask: one bit per pixel of a cell (w×h, row-major), base64 on the wire.
    public let hitMask: Data?

    public init(pack: Pack, sheetPath: String, scale: Int, hitMask: Data?) {
        self.pack = pack
        self.sheetPath = sheetPath
        self.scale = scale
        self.hitMask = hitMask
    }
}

/// UI events produced by the Go core (`dp_poll_event`). See the plan's "Shared contract".
public enum EngineEvent: Sendable, Equatable {
    case state(String)
    case mouth(Int)
    case status(String)
    case transcript(role: String, text: String)
    case error(String)
    case mic(start: Bool)
    case metrics(String)
    /// Re-read `app.state` and rebuild the tray menu.
    case tray
    case pack(PackEvent)
    /// Move the mascot panel (logical top-left); both nil = the default spot.
    case position(x: Double?, y: Double?)
    case hotkey(combo: String)
    /// Audio device UIDs to pin for the next start (nil = system default).
    case devices(input: String?, output: String?)
    case notify(title: String, body: String, sound: String?)
    case canvasOpen(url: String, title: String)
    case settingsChanged
    case restart

    private struct Kind: Decodable { var kind: String }

    private struct Voice: Decodable {
        var state: String?
        var level: Int?
        var text: String?
        var role: String?
    }

    private struct Position: Decodable { var x: Double?, y: Double? }
    private struct Hotkey: Decodable { var combo: String }
    private struct Devices: Decodable { var input: String?, output: String? }
    private struct Notify: Decodable { var title: String, body: String, sound: String? }
    private struct CanvasOpen: Decodable { var url: String, title: String }

    /// Decodes one event JSON object; unknown kinds, malformed JSON and events missing a
    /// required field yield nil.
    public static func decode(_ json: Data) -> EngineEvent? {
        let dec = JSONDecoder()
        guard let k = try? dec.decode(Kind.self, from: json) else { return nil }
        func get<T: Decodable>(_: T.Type) -> T? { try? dec.decode(T.self, from: json) }
        switch k.kind {
        case "state", "mouth", "status", "transcript", "error", "mic", "metrics":
            guard let w = get(Voice.self) else { return nil }
            switch k.kind {
            case "state": return .state(w.state ?? "")
            case "mouth": return .mouth(w.level ?? 0)
            case "status": return .status(w.text ?? "")
            case "transcript": return .transcript(role: w.role ?? "", text: w.text ?? "")
            case "error": return .error(w.text ?? "")
            case "mic": return .mic(start: w.text == "start")
            default: return .metrics(w.text ?? "")
            }
        case "tray": return .tray
        case "settings.changed": return .settingsChanged
        case "restart": return .restart
        case "pack": return get(PackEvent.self).map { .pack($0) }
        case "position": return get(Position.self).map { .position(x: $0.x, y: $0.y) }
        case "hotkey": return get(Hotkey.self).map { .hotkey(combo: $0.combo) }
        case "devices": return get(Devices.self).map { .devices(input: $0.input, output: $0.output) }
        case "notify": return get(Notify.self).map { .notify(title: $0.title, body: $0.body, sound: $0.sound) }
        case "canvas.open": return get(CanvasOpen.self).map { .canvasOpen(url: $0.url, title: $0.title) }
        default: return nil
        }
    }
}
