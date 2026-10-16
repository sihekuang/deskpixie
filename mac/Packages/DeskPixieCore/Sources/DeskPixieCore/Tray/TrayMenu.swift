import Foundation

/// `app.state` (plan "Shared contract"). Every field is optional on the wire so a partial
/// core (before P14) still decodes; missing data hides its rows.
public struct TrayAppState: Decodable, Sendable, Equatable {
    public struct PackRef: Decodable, Sendable, Equatable {
        public var id: String
        public var name: String
    }

    public struct CanvasPage: Decodable, Sendable, Equatable {
        public var slug: String
        public var title: String
        public init(slug: String, title: String) {
            self.slug = slug
            self.title = title
        }
    }

    public struct Job: Decodable, Sendable, Equatable {
        public var id: String
        public var label: String
    }

    /// The fix offered under the Manual-mode status line: `id` runs through `app.action`;
    /// `copy` is put on the clipboard and nothing is run.
    public struct ModeFix: Decodable, Sendable, Equatable {
        public var id: String?
        public var title: String
        public var copy: String?
    }

    public struct Agent: Decodable, Sendable, Equatable {
        public var enabled: Bool
        public var jobs: [Job]

        private enum CodingKeys: String, CodingKey { case enabled, jobs }
        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            enabled = try c.decodeIfPresent(Bool.self, forKey: .enabled) ?? false
            jobs = try c.decodeIfPresent([Job].self, forKey: .jobs) ?? []
        }
    }

    public var state: String
    public var packs: [PackRef]
    public var pack: String
    public var scale: Int?
    public var fade: Bool
    public var talking: Bool
    public var canvasRecent: [CanvasPage]
    public var agent: Agent?
    public var statusLines: [String]
    public var hotkey: String
    public var agentModeFix: ModeFix?

    private enum CodingKeys: String, CodingKey {
        case state, packs, pack, scale, fade, talking, canvasRecent, agent, statusLines, hotkey, agentModeFix
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
        packs = try c.decodeIfPresent([PackRef].self, forKey: .packs) ?? []
        pack = try c.decodeIfPresent(String.self, forKey: .pack) ?? ""
        scale = try c.decodeIfPresent(Int.self, forKey: .scale)
        fade = try c.decodeIfPresent(Bool.self, forKey: .fade) ?? false
        talking = try c.decodeIfPresent(Bool.self, forKey: .talking) ?? false
        canvasRecent = try c.decodeIfPresent([CanvasPage].self, forKey: .canvasRecent) ?? []
        agent = try c.decodeIfPresent(Agent.self, forKey: .agent)
        statusLines = try c.decodeIfPresent([String].self, forKey: .statusLines) ?? []
        hotkey = try c.decodeIfPresent(String.self, forKey: .hotkey) ?? ""
        agentModeFix = try c.decodeIfPresent(ModeFix.self, forKey: .agentModeFix)
    }
}

/// One tray menu entry. Ids are ai-mascot's menu ids; `TrayMenu.isLocal` ones are handled in
/// Swift, every other enabled id goes to `app.action`.
public enum TrayItem: Sendable, Equatable {
    /// Disabled one-line status.
    case label(String)
    case separator
    case action(TrayMenu.Row)
    case submenu(title: String, enabled: Bool, items: [TrayItem])
}

/// Builds the tray tree (ai-mascot `src-tauri/src/tray.rs` `build_menu`) from `app.state`.
public enum TrayMenu {
    /// A clickable (or disabled) row. `checked == nil` = no checkmark slot.
    public struct Row: Sendable, Equatable {
        public var id: String
        public var title: String
        public var checked: Bool?
        public var enabled: Bool
        /// ⌘ + this key.
        public var shortcut: Character?

        public init(id: String, title: String, checked: Bool? = nil, enabled: Bool = true, shortcut: Character? = nil) {
            self.id = id
            self.title = title
            self.checked = checked
            self.enabled = enabled
            self.shortcut = shortcut
        }
    }

    /// Pixie states in display order (mascot-pack `STATE_NAMES`).
    public static let stateNames = ["idle", "listening", "thinking", "delegating", "happy", "error"]
    /// Size presets: label, integer scale.
    public static let sizes: [(label: String, scale: Int)] = [("Small", 4), ("Medium", 6), ("Large", 8), ("Huge", 12)]
    /// Scale assumed when app.state has none (Rust default).
    public static let defaultScale = 8

    public static let quitID = "quit"
    public static let settingsID = "settings"
    public static let micModeID = "mic-mode"
    public static let micModeLabel = "Mic Mode (Voice Isolation)…"

    /// Where a clicked id goes.
    public enum Route: Equatable, Sendable {
        case quit
        /// Opens the Settings window (brought to the front).
        case settings
        /// Opens the Mic Mode picker (`MicModes.show`).
        case micMode
        /// Put this text on the pasteboard; nothing is sent to the core.
        case copy(String)
        /// `app.action {id}`; `focusCanvas` when the user asked for a canvas page, so the
        /// canvas window comes forward (core-initiated opens never take focus).
        case core(String, focusCanvas: Bool = false)
    }

    /// Prefix of a copy-only row's id; the rest is the text to copy.
    /// Reserved: no core action id may start with it (such a row would copy, never reach the core).
    public static let copyPrefix = "copy:"

    public static func route(_ id: String) -> Route {
        if id.hasPrefix(copyPrefix) { return .copy(String(id.dropFirst(copyPrefix.count))) }
        switch id {
        case quitID: return .quit
        case settingsID: return .settings
        case micModeID: return .micMode
        default: return .core(id, focusCanvas: id == "canvas:open" || id.hasPrefix("canvas:page:"))
        }
    }

    /// `build` with the Mic Mode row shown when `micModes` has the picker.
    @MainActor
    public static func build(_ s: TrayAppState?, localStatusLines: [String], micModes: any MicModeSystem,
                             starting: Bool = false) -> [TrayItem] {
        build(s, localStatusLines: localStatusLines, micModeAvailable: micModes.hasPicker, starting: starting)
    }

    /// Ids Swift handles itself (never sent to `app.action`).
    public static func isLocal(_ id: String) -> Bool { [quitID, settingsID, micModeID].contains(id) || id.hasPrefix(copyPrefix) }

    /// Size rows; a non-preset scale gets its own checked "Custom (×N)" row.
    public static func sizeItems(scale: Int) -> [Row] {
        var rows = sizes.map { Row(id: "size:\($0.scale)", title: "\($0.label) (×\($0.scale))", checked: $0.scale == scale) }
        if !sizes.contains(where: { $0.scale == scale }) {
            rows.append(Row(id: "size:\(scale)", title: "Custom (×\(scale))", checked: true))
        }
        return rows
    }

    /// Canvas ▸ Recent rows; titles longer than 60 characters are cut with "…".
    public static func recentItems(_ recent: [TrayAppState.CanvasPage]) -> [Row] {
        let max = 60
        return recent.map { p in
            let label = p.title.count > max ? String(p.title.prefix(max - 1)) + "…" : p.title
            return Row(id: "canvas:page:\(p.slug)", title: label)
        }
    }

    /// The whole menu. `s == nil` (app.state unavailable) leaves only the app's own items.
    /// `localStatusLines` are Swift-side problems (hotkey, audio, engine) shown first.
    /// `starting`: the core is still configuring, so Settings… (which reads the core
    /// synchronously) is disabled until it is ready.
    public static func build(_ s: TrayAppState?, localStatusLines: [String], micModeAvailable: Bool,
                             starting: Bool = false) -> [TrayItem] {
        var items: [TrayItem] = []
        var seen = Set<String>()
        for line in localStatusLines + (s?.statusLines ?? []) where !line.isEmpty && seen.insert(line).inserted {
            items += [.label(line), .separator]
        }
        if let fix = s?.agentModeFix, !fix.title.isEmpty {
            // Right under the status lines; a copy-only fix never reaches app.action.
            let id = fix.id.flatMap { $0.isEmpty ? nil : $0 } ?? fix.copy.map { copyPrefix + $0 }
            if let id {
                let row = TrayItem.action(Row(id: id, title: fix.title))
                if items.isEmpty { items += [row, .separator] } else { items.insert(row, at: items.count - 1) }
            }
        }

        if let s {
            items.append(.submenu(title: "State", enabled: true, items: stateNames.map {
                .action(Row(id: "state:\($0)", title: $0, checked: s.state == $0))
            }))
            items.append(.action(Row(id: "talk", title: "Simulate talking", checked: s.talking)))
            items.append(.submenu(title: "Size", enabled: true,
                                  items: sizeItems(scale: s.scale ?? defaultScale).map { .action($0) }))
            items.append(.action(Row(id: "reset-position", title: "Reset position")))
            items.append(.action(Row(id: "fade", title: "Proximity fade", checked: s.fade)))
            if !s.packs.isEmpty {
                items.append(.submenu(title: "Pixie", enabled: true, items: s.packs.map {
                    .action(Row(id: "pack:\($0.id)", title: $0.name, checked: $0.id == s.pack))
                }))
            }
            items.append(.action(Row(id: "reload", title: "Reload packs")))
            items.append(.submenu(title: "Canvas", enabled: true, items: [
                .action(Row(id: "canvas:open", title: "Open canvas")),
                .submenu(title: "Recent", enabled: !s.canvasRecent.isEmpty,
                         items: recentItems(s.canvasRecent).map { .action($0) }),
                .action(Row(id: "canvas:folder", title: "Open pages folder")),
            ]))
            if let agent = s.agent, agent.enabled {
                var sub: [TrayItem] = agent.jobs.map {
                    .action(Row(id: "agent-job:\($0.id)", title: $0.label, enabled: false))
                }
                sub.append(.submenu(title: "Cancel job", enabled: !agent.jobs.isEmpty, items: agent.jobs.map {
                    .action(Row(id: "agent:cancel:\($0.id)", title: $0.label))
                }))
                sub.append(.action(Row(id: "agent:open", title: "Open coordinator terminal")))
                sub.append(.action(Row(id: "agent:restart", title: "Restart coordinator")))
                items.append(.submenu(title: "Coordinator", enabled: true, items: sub))
            }
            items.append(.separator)
        }

        if micModeAvailable {
            items.append(.action(Row(id: micModeID, title: micModeLabel)))
        }
        items.append(.action(Row(id: settingsID, title: "Settings…", enabled: !starting, shortcut: ",")))
        items.append(.separator)
        items.append(.action(Row(id: quitID, title: "Quit")))
        return items
    }
}
