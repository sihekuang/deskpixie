import Foundation

/// One agent beside Pixie, as the core's `agents` event lists it (#91).
public struct RosterRow: Decodable, Sendable, Equatable, Identifiable {
    public let name: String
    public let label: String
    public let detail: String?
    /// `PaneStatus`: working | idle | blocked | done | unknown.
    public let status: String
    /// coordinator | worker | pane | session.
    public let kind: String
    public let reachable: Bool
    /// A session's terminal app bundle id; nil when unknown.
    public let app: String?
    /// It is working and can be reached: the core offers a stop button for it (#309).
    public let stoppable: Bool
    public var id: String { name }

    public init(name: String, label: String, detail: String?, status: String, kind: String, reachable: Bool, app: String?,
                stoppable: Bool = false) {
        self.name = name
        self.label = label
        self.detail = detail
        self.status = status
        self.kind = kind
        self.reachable = reachable
        self.app = app
        self.stoppable = stoppable
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(name: try c.decode(String.self, forKey: .name), label: try c.decode(String.self, forKey: .label),
                  detail: try c.decodeIfPresent(String.self, forKey: .detail), status: try c.decode(String.self, forKey: .status),
                  kind: try c.decode(String.self, forKey: .kind), reachable: try c.decode(Bool.self, forKey: .reachable),
                  app: try c.decodeIfPresent(String.self, forKey: .app),
                  // Left out when false.
                  stoppable: try c.decodeIfPresent(Bool.self, forKey: .stoppable) ?? false)
    }

    private enum CodingKeys: String, CodingKey { case name, label, detail, status, kind, reachable, app, stoppable }
}

/// The whole roster. `agentCount` never counts the coordinator; `yourCount` only reachable sessions.
/// `busy`: something is working, as the core decides (#234).
public struct RosterView: Decodable, Sendable, Equatable {
    public let agents: [RosterRow]
    public let yours: [RosterRow]
    public let agentCount: Int
    public let yourCount: Int
    public let busy: Bool

    public static let empty = RosterView(agents: [], yours: [], agentCount: 0, yourCount: 0)

    public init(agents: [RosterRow], yours: [RosterRow], agentCount: Int, yourCount: Int, busy: Bool = false) {
        self.agents = agents
        self.yours = yours
        self.agentCount = agentCount
        self.yourCount = yourCount
        self.busy = busy
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(agents: try c.decode([RosterRow].self, forKey: .agents),
                  yours: try c.decode([RosterRow].self, forKey: .yours),
                  agentCount: try c.decode(Int.self, forKey: .agentCount),
                  yourCount: try c.decode(Int.self, forKey: .yourCount),
                  busy: try c.decodeIfPresent(Bool.self, forKey: .busy) ?? false)
    }

    private enum CodingKeys: String, CodingKey {
        case agents, yours, busy
        case agentCount = "agent_count"
        case yourCount = "your_count"
    }
}

/// The chip under Pixie and the list it opens (spec docs/superpowers/specs/2026-10-15-agent-roster-design.md).
/// Pure: the controller feeds it the core's roster and the pointer, and draws it.
public struct AgentRosterModel: Sendable, Equatable {
    public static let maxRows = 8

    public struct Section: Sendable, Equatable {
        public let title: String
        public let rows: [RosterRow]
        /// The "+N more" row's N.
        public let more: Int
    }

    public private(set) var view: RosterView = .empty
    /// The list is open (pointer over the chip, or a click). Closing it clears `notice`.
    public var open = false {
        didSet { if !open { notice = nil } }
    }
    /// Why the last stop didn't work, in the core's words, under the list until it closes.
    public private(set) var notice: String?
    /// The rows whose stop call is on its way: their button waits (a second Esc would open a
    /// menu in the agent, #309).
    public private(set) var stopping: Set<String> = []

    public init() {}

    public mutating func apply(_ v: RosterView) {
        view = v
        if !visible { open = false }
    }

    /// Anything counted: the coordinator alone shows no chip.
    public var visible: Bool { view.agentCount + view.yourCount > 0 }

    /// The chip's pulsing dot (#234): something is working, while the chip shows.
    public var busy: Bool { visible && view.busy }

    /// `⚙ 3 · ⌨ 2`; a zero side is left out.
    public var chipText: String {
        var parts: [String] = []
        if view.agentCount > 0 { parts.append("⚙ \(view.agentCount)") }
        if view.yourCount > 0 { parts.append("⌨ \(view.yourCount)") }
        return parts.joined(separator: " · ")
    }

    public var sections: [Section] {
        var out: [Section] = []
        if !view.agents.isEmpty { out.append(section("Pixie's agents (\(view.agentCount))", view.agents)) }
        if !view.yours.isEmpty { out.append(section("Your sessions (\(view.yourCount))", view.yours)) }
        return out
    }

    private func section(_ title: String, _ rows: [RosterRow]) -> Section {
        Section(title: title, rows: Array(rows.prefix(Self.maxRows)), more: max(0, rows.count - Self.maxRows))
    }

    /// The caption's buttons (#261): the named sessions of yours still open and reachable, in the
    /// order named, each once.
    public static func buttons(for names: [String], in view: RosterView) -> [RosterRow] {
        var out: [RosterRow] = []
        for name in names where !out.contains(where: { $0.name == name }) {
            if let r = view.yours.first(where: { $0.name == name }), r.kind == "session", clickable(r) {
                out.append(r)
            }
        }
        return out
    }

    /// Pixie's agents always; your sessions only when reachable with a known app.
    public static func clickable(_ r: RosterRow) -> Bool {
        r.kind != "session" || r.reachable
    }

    /// The square stop button on a row (#309): only while the core says it can be stopped.
    public static func showsStop(_ r: RosterRow) -> Bool { r.stoppable }

    /// A click on a row's stop button: true if its call should go now (none is on its way).
    public mutating func beginStop(_ name: String) -> Bool {
        stopping.insert(name).inserted
    }

    /// The row's stop call came back: a refusal shows under the list, while it is open.
    public mutating func endStop(_ name: String, _ result: Result<Void, Error>) {
        stopping.remove(name)
        if case .failure(let err) = result { show(Self.stopFailure(err)) }
    }

    /// `agent.stopFailed`: the stop went but didn't work.
    public mutating func stopFailed(_ text: String) { show(text) }

    /// Only while the list is open: a closed list's notice would greet its next opening.
    private mutating func show(_ text: String) {
        if open { notice = text }
    }

    /// A failed `agent.interrupt` call in words to show (the roster and the chat window): the
    /// core's own refusal as it is, anything else "Couldn't reach Pixie. Try again.".
    public static func stopFailure(_ err: Error) -> String {
        if case DeskPixieEngineError.refused(let why) = err { return why }
        return "Couldn't reach Pixie. Try again."
    }

    /// "Stop docs-writer": the stop button's tooltip and VoiceOver name.
    public static func stopLabel(_ r: RosterRow) -> String { "Stop \(r.label.isEmpty ? r.name : r.label)" }

    public static func dot(_ status: String) -> String {
        switch status {
        case "working": return "●"
        case "blocked": return "◐"
        case "idle", "done": return "○"
        default: return "·"
        }
    }
}
