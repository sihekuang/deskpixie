import Foundation
import Observation

/// Settings ▸ About (#295), Go `app.About`: which DeskPixie, core and pix are running.
public struct AboutInfo: Codable, Equatable, Sendable {
    public struct Row: Codable, Equatable, Sendable {
        public var label: String
        public var value: String

        public init(label: String, value: String) {
            self.label = label
            self.value = value
        }
    }

    public var rows: [Row]
    /// About the terminal's pix, and what to do.
    public var note: String?
    /// The note is a problem to fix (the terminal's pix isn't the app's), not just a hint.
    public var warning: Bool
    /// What Copy puts on the pasteboard.
    public var text: String

    public init(rows: [Row], note: String? = nil, warning: Bool = false, text: String) {
        self.rows = rows
        self.note = note
        self.warning = warning
        self.text = text
    }

    enum CodingKeys: String, CodingKey { case rows, note, warning, text }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        rows = try c.decode([Row].self, forKey: .rows)
        note = try c.decodeIfPresent(String.self, forKey: .note)
        warning = try c.decodeIfPresent(Bool.self, forKey: .warning) ?? false
        text = try c.decode(String.self, forKey: .text)
    }
}

/// What only the app knows, for `app.about`.
public struct AboutArgs: Encodable, Equatable, Sendable {
    /// The app's version, e.g. "0.19.0 (412)".
    public var app: String
    /// E.g. "macOS 15.1.0".
    public var os: String
    /// The pix inside the app.
    public var pix: String
    /// The login shell's PATH ("" when it couldn't be read).
    public var path: String

    public init(app: String, os: String, pix: String, path: String) {
        self.app = app
        self.os = os
        self.pix = pix
        self.path = path
    }
}

/// `app.about`. Sendable: the core runs pix to ask its version, so it is called off main.
public protocol AboutClient: Sendable {
    func about(_ args: AboutArgs) throws -> AboutInfo
}

extension EngineSettingsClient: AboutClient {
    public func about(_ args: AboutArgs) throws -> AboutInfo {
        try engine.call("app.about", args, as: AboutInfo.self)
    }
}

/// The About page's state: nothing until the first load, then the core's answer or why it failed.
@MainActor
@Observable
public final class AboutModel {
    public private(set) var info: AboutInfo?
    public private(set) var error: String?
    @ObservationIgnored private let client: any AboutClient
    /// Counts loads, so an older one that finishes last doesn't overwrite a newer one.
    @ObservationIgnored private var loads = 0

    public init(client: any AboutClient) { self.client = client }

    /// Asks the core again; `args` is built off main too (reading the login shell's PATH runs it).
    public func load(_ args: @escaping @Sendable () -> AboutArgs) async {
        let client = self.client
        loads += 1
        let load = loads
        let result = await Task.detached { Result { try client.about(args()) } }.value
        guard load == loads else { return }
        switch result {
        case .success(let info):
            self.info = info
            error = nil
        case .failure(let error):
            self.error = "Couldn't read the versions: \(error.localizedDescription)"
        }
    }
}
