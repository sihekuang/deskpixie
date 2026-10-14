import Foundation

/// The wire types of the `settings.*` dp_call methods (Go `core/settings`: Snapshot,
/// FieldView, KindView, OptionView, SetError, Secrets).

/// A platform the window styles itself for. Unknown names decode as themselves.
public struct SettingsPlatform: RawRepresentable, Codable, Hashable, Sendable {
    public var rawValue: String
    public init(rawValue: String) { self.rawValue = rawValue }

    public static let macos = SettingsPlatform(rawValue: "macos")
    public static let windows = SettingsPlatform(rawValue: "windows")
    public static let linux = SettingsPlatform(rawValue: "linux")
    public static let all: [SettingsPlatform] = [.macos, .windows, .linux]

    public init(from decoder: Decoder) throws {
        rawValue = try decoder.singleValueContainer().decode(String.self)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(rawValue)
    }
}

/// A Settings window tab.
public enum SettingsSection: String, Codable, Sendable, CaseIterable {
    case mascot = "Mascot"
    case voice = "Voice"
    case agent = "Agent"
    case workers = "Workers"
    case canvas = "Canvas"
    case advanced = "Advanced"
}

/// One choice. `platforms` nil means everywhere (the web page's `availableOn`).
public struct SettingsOption: Codable, Equatable, Sendable {
    public var value: String
    public var label: String
    public var platforms: [SettingsPlatform]?

    public init(value: String, label: String, platforms: [SettingsPlatform]? = nil) {
        self.value = value
        self.label = label
        self.platforms = platforms
    }
}

/// A field's control kind, options resolved (Go `KindView`, tagged by `type`).
public enum SettingsKind: Equatable, Sendable, Codable {
    case bool
    case int(min: Int64, max: Int64, menu: Bool)
    case choice([SettingsOption])
    case multiChoice([SettingsOption])
    case text(optional: Bool)
    case path(dir: Bool, optional: Bool)
    case hotkey

    private enum Keys: String, CodingKey { case type, min, max, menu, options, optional, dir }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        let type = try c.decode(String.self, forKey: .type)
        switch type {
        case "bool": self = .bool
        case "int":
            self = .int(min: try c.decode(Int64.self, forKey: .min), max: try c.decode(Int64.self, forKey: .max),
                        menu: try c.decodeIfPresent(Bool.self, forKey: .menu) ?? false)
        case "choice": self = .choice(try c.decodeIfPresent([SettingsOption].self, forKey: .options) ?? [])
        case "multiChoice": self = .multiChoice(try c.decodeIfPresent([SettingsOption].self, forKey: .options) ?? [])
        case "text": self = .text(optional: try c.decodeIfPresent(Bool.self, forKey: .optional) ?? false)
        case "path":
            self = .path(dir: try c.decodeIfPresent(Bool.self, forKey: .dir) ?? false,
                         optional: try c.decodeIfPresent(Bool.self, forKey: .optional) ?? false)
        case "hotkey": self = .hotkey
        default:
            throw DecodingError.dataCorruptedError(forKey: .type, in: c, debugDescription: "unknown kind \(type)")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: Keys.self)
        switch self {
        case .bool: try c.encode("bool", forKey: .type)
        case .int(let min, let max, let menu):
            try c.encode("int", forKey: .type)
            try c.encode(min, forKey: .min)
            try c.encode(max, forKey: .max)
            try c.encode(menu, forKey: .menu)
        case .choice(let o):
            try c.encode("choice", forKey: .type)
            try c.encode(o, forKey: .options)
        case .multiChoice(let o):
            try c.encode("multiChoice", forKey: .type)
            try c.encode(o, forKey: .options)
        case .text(let optional):
            try c.encode("text", forKey: .type)
            try c.encode(optional, forKey: .optional)
        case .path(let dir, let optional):
            try c.encode("path", forKey: .type)
            try c.encode(dir, forKey: .dir)
            try c.encode(optional, forKey: .optional)
        case .hotkey: try c.encode("hotkey", forKey: .type)
        }
    }
}

/// One field for the window (Go `FieldView`).
public struct SettingsField: Codable, Equatable, Sendable {
    public var path: String
    public var section: SettingsSection
    public var label: String
    public var help: String
    public var kind: SettingsKind
    /// "live" or "restart".
    public var apply: String
    public var platforms: [SettingsPlatform]?

    public init(path: String, section: SettingsSection, label: String, help: String = "", kind: SettingsKind,
                apply: String = "live", platforms: [SettingsPlatform]? = SettingsPlatform.all) {
        self.path = path
        self.section = section
        self.label = label
        self.help = help
        self.kind = kind
        self.apply = apply
        self.platforms = platforms
    }
}

/// An error that belongs under a field.
public struct SettingsFieldError: Codable, Equatable, Sendable {
    public var path: String
    public var message: String

    public init(path: String, message: String) {
        self.path = path
        self.message = message
    }
}

/// Why a set changed nothing: `{"kind":"invalid","errors":[…]}` or `{"kind":"save","message":…}`.
public enum SettingsSetError: Equatable, Sendable, Codable {
    case invalid([SettingsFieldError])
    case save(String)

    private enum Keys: String, CodingKey { case kind, errors, message }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        if try c.decode(String.self, forKey: .kind) == "invalid" {
            self = .invalid(try c.decodeIfPresent([SettingsFieldError].self, forKey: .errors) ?? [])
        } else {
            self = .save(try c.decodeIfPresent(String.self, forKey: .message) ?? "")
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: Keys.self)
        switch self {
        case .invalid(let errors):
            try c.encode("invalid", forKey: .kind)
            try c.encode(errors, forKey: .errors)
        case .save(let message):
            try c.encode("save", forKey: .kind)
            try c.encode(message, forKey: .message)
        }
    }

    /// Every message, joined (Rust `message_of`).
    public var message: String {
        switch self {
        case .invalid(let errors): return errors.map(\.message).joined(separator: " ")
        case .save(let m): return m
        }
    }
}

/// What the Settings window shows (Go `Snapshot`).
public struct SettingsSnapshot: Codable, Equatable, Sendable {
    public var platform: SettingsPlatform
    public var values: JSONValue
    public var fields: [SettingsField]
    /// Restart fields that differ from the startup config, by path.
    public var restartPending: [String]
    public var fileError: String?
    public var warning: String?
    public var seq: UInt64
    /// Present on the snapshot `settings.set` returns when the set changed nothing.
    public var setError: SettingsSetError?

    public init(platform: SettingsPlatform = .macos, values: JSONValue = [:], fields: [SettingsField] = [],
                restartPending: [String] = [], fileError: String? = nil, warning: String? = nil, seq: UInt64 = 0,
                setError: SettingsSetError? = nil) {
        self.platform = platform
        self.values = values
        self.fields = fields
        self.restartPending = restartPending
        self.fileError = fileError
        self.warning = warning
        self.seq = seq
        self.setError = setError
    }

    private enum Keys: String, CodingKey {
        case platform, values, fields, restartPending, fileError, warning, seq, setError
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        platform = try c.decodeIfPresent(SettingsPlatform.self, forKey: .platform) ?? .macos
        values = try c.decodeIfPresent(JSONValue.self, forKey: .values) ?? [:]
        fields = try c.decodeIfPresent([SettingsField].self, forKey: .fields) ?? []
        restartPending = try c.decodeIfPresent([String].self, forKey: .restartPending) ?? []
        fileError = try c.decodeIfPresent(String.self, forKey: .fileError)
        warning = try c.decodeIfPresent(String.self, forKey: .warning)
        seq = try c.decodeIfPresent(UInt64.self, forKey: .seq) ?? 0
        setError = try c.decodeIfPresent(SettingsSetError.self, forKey: .setError)
    }
}

/// Whether an API key exists; never the key.
public enum SettingsKeyStatus: String, Codable, Sendable {
    case found, missing
}

/// `settings.secrets`.
public struct SettingsSecrets: Codable, Equatable, Sendable {
    public var openai: SettingsKeyStatus?
    public var gemini: SettingsKeyStatus?

    public init(openai: SettingsKeyStatus?, gemini: SettingsKeyStatus?) {
        self.openai = openai
        self.gemini = gemini
    }

    public subscript(id: SettingsSecretID) -> SettingsKeyStatus? {
        switch id {
        case .openAI: return openai
        case .gemini: return gemini
        }
    }
}
