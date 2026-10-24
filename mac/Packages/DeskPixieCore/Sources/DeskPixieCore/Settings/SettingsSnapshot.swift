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

/// A Settings window page (Go `Section`), in sidebar order; the raw value is its title.
public enum SettingsSection: String, Codable, Sendable, CaseIterable {
    case general = "General"
    case voice = "Voice"
    case coordinator = "Coordinator"
    case sessions = "Sessions"
    case workers = "Workers"
    case notifications = "Notifications"
    case advanced = "Advanced"
}

/// Where one of the window's other rows sits (Go `ExtraView`): its page and group. The row's
/// look is `SettingsLayout.extra(id)`.
public struct SettingsExtraPlace: Codable, Equatable, Sendable {
    public var id: String
    public var section: SettingsSection
    public var group: String

    public init(id: String, section: SettingsSection, group: String) {
        self.id = id
        self.section = section
        self.group = group
    }
}

/// One search result (Go `Hit`, #287): a setting, or one of the window's other rows, whose
/// path is `extra:` and its id (`SettingsLayout.Extra.id`).
public struct SettingsHit: Codable, Equatable, Sendable {
    public var path: String
    public var section: SettingsSection
    /// The group heading it sits under (#290); "" from a core that has none.
    public var group: String
    public var label: String
    public var help: String

    public init(path: String, section: SettingsSection, group: String = "", label: String, help: String = "") {
        self.path = path
        self.section = section
        self.group = group
        self.label = label
        self.help = help
    }

    private enum Keys: String, CodingKey { case path, section, group, label, help }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        path = try c.decode(String.self, forKey: .path)
        section = try c.decode(SettingsSection.self, forKey: .section)
        group = try c.decodeIfPresent(String.self, forKey: .group) ?? ""
        label = try c.decode(String.self, forKey: .label)
        help = try c.decodeIfPresent(String.self, forKey: .help) ?? ""
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: Keys.self)
        try c.encode(path, forKey: .path)
        try c.encode(section, forKey: .section)
        try c.encode(group, forKey: .group)
        try c.encode(label, forKey: .label)
        try c.encode(help, forKey: .help)
    }
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

/// Holds while the field at `key` has one of the values `in` (Go `Condition`). Values compare
/// as the window shows them: a string as itself, a bool as "true"/"false", a number in decimal.
public struct SettingsCondition: Codable, Equatable, Sendable {
    public var key: String
    public var `in`: [String]

    public init(key: String, in values: [String]) {
        self.key = key
        self.in = values
    }

    /// Whether the condition holds for `values` (a missing value reads as "").
    public func holds(_ values: JSONValue) -> Bool {
        `in`.contains(SettingsLayout.textOf(values.at(key)))
    }
}

/// One field for the window (Go `FieldView`).
public struct SettingsField: Codable, Equatable, Sendable {
    public var path: String
    public var section: SettingsSection
    /// The group heading it sits under on its page (#290).
    public var group: String
    public var label: String
    public var help: String
    public var kind: SettingsKind
    /// "live" or "restart".
    public var apply: String
    public var platforms: [SettingsPlatform]?
    /// The field shows only while every condition holds; nil (absent) means always.
    public var visibleWhen: [SettingsCondition]?
    /// The field can be changed only while every condition holds (a master switch is on);
    /// otherwise it is greyed out. Nil (absent) means always.
    public var enabledWhen: [SettingsCondition]?
    /// Shown after a number ("seconds"); from the kind's `unit`.
    public var unit: String?

    public init(path: String, section: SettingsSection, group: String = "", label: String, help: String = "",
                kind: SettingsKind, apply: String = "live", platforms: [SettingsPlatform]? = SettingsPlatform.all,
                visibleWhen: [SettingsCondition]? = nil, enabledWhen: [SettingsCondition]? = nil, unit: String? = nil) {
        self.path = path
        self.section = section
        self.group = group
        self.label = label
        self.help = help
        self.kind = kind
        self.apply = apply
        self.platforms = platforms
        self.visibleWhen = visibleWhen
        self.enabledWhen = enabledWhen
        self.unit = unit
    }

    private enum CodingKeys: String, CodingKey {
        case path, section, group, label, help, kind, apply, platforms, visibleWhen, enabledWhen
    }

    private enum UnitKey: String, CodingKey { case unit }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(String.self, forKey: .path)
        section = try c.decode(SettingsSection.self, forKey: .section)
        group = try c.decodeIfPresent(String.self, forKey: .group) ?? ""
        label = try c.decode(String.self, forKey: .label)
        help = try c.decodeIfPresent(String.self, forKey: .help) ?? ""
        kind = try c.decode(SettingsKind.self, forKey: .kind)
        apply = try c.decodeIfPresent(String.self, forKey: .apply) ?? "live"
        platforms = try c.decodeIfPresent([SettingsPlatform].self, forKey: .platforms)
        visibleWhen = try c.decodeIfPresent([SettingsCondition].self, forKey: .visibleWhen)
        enabledWhen = try c.decodeIfPresent([SettingsCondition].self, forKey: .enabledWhen)
        unit = try c.nestedContainer(keyedBy: UnitKey.self, forKey: .kind).decodeIfPresent(String.self, forKey: .unit)
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(path, forKey: .path)
        try c.encode(section, forKey: .section)
        try c.encode(group, forKey: .group)
        try c.encode(label, forKey: .label)
        try c.encode(help, forKey: .help)
        // The core sends the unit inside the kind; write it back there.
        let kindEncoder = c.superEncoder(forKey: .kind)
        try kind.encode(to: kindEncoder)
        if let unit {
            var k = kindEncoder.container(keyedBy: UnitKey.self)
            try k.encode(unit, forKey: .unit)
        }
        try c.encode(apply, forKey: .apply)
        try c.encodeIfPresent(platforms, forKey: .platforms)
        try c.encodeIfPresent(visibleWhen, forKey: .visibleWhen)
        try c.encodeIfPresent(enabledWhen, forKey: .enabledWhen)
    }

    /// Whether the field can be changed for `values`: every `enabledWhen` condition holds.
    public func isEnabled(_ values: JSONValue) -> Bool {
        (enabledWhen ?? []).allSatisfy { $0.holds(values) }
    }

    /// Whether the field shows for `values`: every `visibleWhen` condition holds. A hidden
    /// field keeps its stored value.
    public func isVisible(_ values: JSONValue) -> Bool {
        (visibleWhen ?? []).allSatisfy { $0.holds(values) }
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
    /// Where the window's other rows sit (#290).
    public var extras: [SettingsExtraPlace]
    /// Restart fields that differ from the startup config, by path.
    public var restartPending: [String]
    public var fileError: String?
    public var warning: String?
    public var seq: UInt64
    /// Present on the snapshot `settings.set` returns when the set changed nothing.
    public var setError: SettingsSetError?

    public init(platform: SettingsPlatform = .macos, values: JSONValue = [:], fields: [SettingsField] = [],
                extras: [SettingsExtraPlace] = [], restartPending: [String] = [], fileError: String? = nil, warning: String? = nil, seq: UInt64 = 0,
                setError: SettingsSetError? = nil) {
        self.platform = platform
        self.values = values
        self.fields = fields
        self.extras = extras
        self.restartPending = restartPending
        self.fileError = fileError
        self.warning = warning
        self.seq = seq
        self.setError = setError
    }

    private enum Keys: String, CodingKey {
        case platform, values, fields, extras, restartPending, fileError, warning, seq, setError
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        platform = try c.decodeIfPresent(SettingsPlatform.self, forKey: .platform) ?? .macos
        values = try c.decodeIfPresent(JSONValue.self, forKey: .values) ?? [:]
        fields = try c.decodeIfPresent([SettingsField].self, forKey: .fields) ?? []
        extras = try c.decodeIfPresent([SettingsExtraPlace].self, forKey: .extras) ?? []
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

/// Where each key comes from: "env", "keychain", "dotenv", or "" (missing).
public struct SettingsKeySources: Codable, Equatable, Sendable {
    public var openai: String?
    public var gemini: String?

    public init(openai: String?, gemini: String?) {
        self.openai = openai
        self.gemini = gemini
    }
}

/// `settings.secrets`.
public struct SettingsSecrets: Codable, Equatable, Sendable {
    public var openai: SettingsKeyStatus?
    public var gemini: SettingsKeyStatus?
    /// Nil from a core that does not name sources.
    public var sources: SettingsKeySources?

    public init(openai: SettingsKeyStatus?, gemini: SettingsKeyStatus?, sources: SettingsKeySources? = nil) {
        self.openai = openai
        self.gemini = gemini
        self.sources = sources
    }

    public subscript(id: SettingsSecretID) -> SettingsKeyStatus? {
        switch id {
        case .openAI: return openai
        case .gemini: return gemini
        }
    }

    /// Where the key comes from ("env", "keychain", "dotenv"); nil or "" when unknown or missing.
    public func source(_ id: SettingsSecretID) -> String? {
        switch id {
        case .openAI: return sources?.openai
        case .gemini: return sources?.gemini
        }
    }
}
