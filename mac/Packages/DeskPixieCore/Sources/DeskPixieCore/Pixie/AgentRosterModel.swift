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
    public var id: String { name }

    public init(name: String, label: String, detail: String?, status: String, kind: String, reachable: Bool, app: String?) {
        self.name = name
        self.label = label
        self.detail = detail
        self.status = status
        self.kind = kind
        self.reachable = reachable
        self.app = app
    }
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
    /// The list is open (pointer over the chip, or a click).
    public var open = false

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

    public static func dot(_ status: String) -> String {
        switch status {
        case "working": return "●"
        case "blocked": return "◐"
        case "idle", "done": return "○"
        default: return "·"
        }
    }
}
