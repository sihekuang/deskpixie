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
    /// `secrets.keychain {values}`: the keys stored in the Keychain (variable → key), replacing
    /// the set the core holds. Never logged.
    func setKeychain(_ values: [String: String]) throws
    /// `settings.search {query}`: the matching settings and rows, best first (#287).
    func search(_ query: String) throws -> [SettingsHit]
    /// `settings.openFile`: shows settings.json in Finder.
    func openFile() throws
    /// `settings.restart`: quits and relaunches.
    func restart() throws
    /// `app.action {id: "agent:restart"}`: restarts the coordinator (#179; the view asks first).
    func restartCoordinator() throws
    /// `app.action {id: "reload"}`: reads the pixies again, so one just added to your pixies
    /// folder is in the Pixie list (#266).
    func reloadPixies() throws
    /// `logs.export {path, app, os}`: the core writes the logs' zip at path (#225).
    func exportLogs(to path: String, app: String, os: String) throws
    /// `setup.projectsFolder`: the projects folder checked on disk, changing nothing (#216).
    func projectsFolder() throws -> ProjectsFolderStatus
    /// `setup.createProjectsFolder`: creates it when it is missing; answers the check after.
    func createProjectsFolder() throws -> ProjectsFolderStatus
}

/// `SettingsClient` over the Go core's `dp_call`.
public struct EngineSettingsClient: SettingsClient, Sendable {
    let engine: CoreEngine

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

    private struct KeychainArgs: Encodable {
        let values: [String: String]
    }

    public func setKeychain(_ values: [String: String]) throws {
        _ = try engine.call("secrets.keychain", KeychainArgs(values: values))
    }

    private struct SearchArgs: Encodable {
        let query: String
    }

    public func search(_ query: String) throws -> [SettingsHit] {
        try engine.call("settings.search", SearchArgs(query: query), as: [SettingsHit].self)
    }

    public func openFile() throws {
        _ = try engine.call("settings.openFile")
    }

    public func restart() throws {
        _ = try engine.call("settings.restart")
    }

    private struct ActionArgs: Encodable {
        let id: String
    }

    public func restartCoordinator() throws {
        _ = try engine.call("app.action", ActionArgs(id: CoordinatorRestart.actionID))
    }

    public func reloadPixies() throws {
        _ = try engine.call("app.action", ActionArgs(id: "reload"))
    }

    private struct ExportArgs: Encodable {
        let path: String
        let app: String
        let os: String
    }

    public func exportLogs(to path: String, app: String, os: String) throws {
        _ = try engine.call("logs.export", ExportArgs(path: path, app: app, os: os))
    }

    public func projectsFolder() throws -> ProjectsFolderStatus {
        try engine.call("setup.projectsFolder", as: ProjectsFolderStatus.self)
    }

    public func createProjectsFolder() throws -> ProjectsFolderStatus {
        try engine.call("setup.createProjectsFolder", as: ProjectsFolderStatus.self)
    }
}
