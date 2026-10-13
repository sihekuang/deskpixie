import Foundation

/// The `settings.*` dp_call methods the Settings window uses (Go `core/settings` API).
public protocol SettingsClient {
    /// `settings.snapshot`: picks up hand edits, then the current view.
    func snapshot() throws -> SettingsSnapshot
    /// `settings.reload`: the window got focus or the core said settings changed.
    func reload() throws -> SettingsSnapshot
    /// `settings.set {field, value}`: a refused set returns a snapshot with `setError`.
    func set(_ field: String, _ value: JSONValue) throws -> SettingsSnapshot
    /// `settings.secrets`: found or missing per provider.
    func secrets() throws -> SettingsSecrets
    /// `settings.openFile`: shows settings.json in Finder.
    func openFile() throws
    /// `settings.restart`: quits and relaunches.
    func restart() throws
}

/// `SettingsClient` over the Go core's `dp_call`.
public struct EngineSettingsClient: SettingsClient, Sendable {
    private let engine: CoreEngine

    public init(engine: CoreEngine) { self.engine = engine }

    private struct SetArgs: Encodable {
        let field: String
        let value: JSONValue
    }

    public func snapshot() throws -> SettingsSnapshot {
        try engine.call("settings.snapshot", as: SettingsSnapshot.self)
    }

    public func reload() throws -> SettingsSnapshot {
        try engine.call("settings.reload", as: SettingsSnapshot.self)
    }

    public func set(_ field: String, _ value: JSONValue) throws -> SettingsSnapshot {
        try engine.call("settings.set", SetArgs(field: field, value: value), as: SettingsSnapshot.self)
    }

    public func secrets() throws -> SettingsSecrets {
        try engine.call("settings.secrets", as: SettingsSecrets.self)
    }

    public func openFile() throws {
        _ = try engine.call("settings.openFile")
    }

    public func restart() throws {
        _ = try engine.call("settings.restart")
    }
}
