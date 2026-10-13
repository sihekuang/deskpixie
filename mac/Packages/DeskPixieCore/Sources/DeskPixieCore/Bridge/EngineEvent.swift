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

/// UI events produced by the Go core (`dp_poll_event`).
public enum EngineEvent: Sendable, Equatable {
    case state(String)
    case mouth(Int)
    case status(String)
    case transcript(role: String, text: String)
    case error(String)
    case mic(start: Bool)
    case metrics(String)

    private struct Wire: Decodable {
        var kind: String
        var state: String?
        var level: Int?
        var text: String?
        var role: String?
    }

    /// Decodes one UIEvent JSON object; unknown kinds and malformed JSON yield nil.
    public static func decode(_ json: Data) -> EngineEvent? {
        guard let w = try? JSONDecoder().decode(Wire.self, from: json) else { return nil }
        switch w.kind {
        case "state": return .state(w.state ?? "")
        case "mouth": return .mouth(w.level ?? 0)
        case "status": return .status(w.text ?? "")
        case "transcript": return .transcript(role: w.role ?? "", text: w.text ?? "")
        case "error": return .error(w.text ?? "")
        case "mic": return .mic(start: w.text == "start")
        case "metrics": return .metrics(w.text ?? "")
        default: return nil
        }
    }
}
