import Foundation

/// The first-run window (#149, spec docs/superpowers/specs/2026-10-17-onboarding-design.md):
/// welcome, choose features, set up each chosen one, done. Pure: the window controller feeds it
/// the settings snapshot and what it read (key status, microphone, programs, pix), and draws it.

/// A feature the window offers, in the order it shows them.
public enum OnboardingFeature: String, CaseIterable, Sendable {
    case voice, agents, sessions, canvas

    public var title: String {
        switch self {
        case .voice: return "Talk to Pixie"
        case .agents: return "Pixie runs agents for you"
        case .sessions: return "Watch your own sessions"
        case .canvas: return "Show canvas pages as they're made"
        }
    }

    public var summary: String {
        switch self {
        case .voice: return "Speak with Pixie from any app. This is DeskPixie, so it's always on."
        case .agents: return "Pixie hands real work to a coordinator agent and its workers."
        case .sessions: return "Pixie tells you when the sessions you start with pix finish, and shows their questions."
        case .canvas: return "Pages Pixie writes to show you things open on their own."
        }
    }

    /// The Done line when nothing is missing.
    public var readyText: String {
        switch self {
        case .voice: return "Press your voice hotkey, or double-click Pixie, and say hello."
        case .agents: return "Ask Pixie for real work and it hands it to the coordinator."
        case .sessions: return "Start a session with pix claude or pix codex."
        case .canvas: return "New pages open as Pixie makes them."
        }
    }

    /// The setting its switch is; nil when it is always on.
    public var settingPath: String? {
        switch self {
        case .voice: return nil
        case .agents: return "agent.enabled"
        case .sessions: return "notifications.sessionTurns"
        case .canvas: return "canvas.autoOpen"
        }
    }

    /// Canvas has nothing to set up beyond its switch.
    public var hasSetup: Bool { self != .canvas }

    /// The SF Symbol on its card.
    public var symbol: String {
        switch self {
        case .voice: return "waveform"
        case .agents: return "sparkles"
        case .sessions: return "terminal"
        case .canvas: return "square.on.square"
        }
    }
}

/// Five steps: welcome; pick your Pixie (#212); set up, where each feature's card holds its switch
/// and, while it is on, its setup; permissions, every one asked for at once (#210); done.
public enum OnboardingPage: Equatable, Sendable, CaseIterable {
    case welcome, pixie, setup, permissions, done
}

/// The pick-your-Pixie step (#212).
public enum OnboardingPixies {
    /// A pack's folder, for its preview: the last root that has it, as the core loads them (a
    /// pack in the user's folder replaces a bundled one of the same name). nil when none has it.
    public static func folder(id: String, roots: [URL], exists: (URL) -> Bool) -> URL? {
        guard !id.isEmpty, !id.contains("/") else { return nil }
        return roots.reversed().map { $0.appendingPathComponent(id, isDirectory: true) }
            .first { exists($0.appendingPathComponent("pixie.json")) }
    }
}

/// Where the window is.
public struct OnboardingFlow: Equatable, Sendable {
    public static let pages = OnboardingPage.allCases

    public private(set) var index = 0

    public init() {}

    /// The features on in `values` (settings.json's values), voice always first.
    public static func chosen(_ values: JSONValue) -> [OnboardingFeature] {
        OnboardingFeature.allCases.filter { f in
            guard let path = f.settingPath else { return true }
            return values.at(path)?.boolValue ?? Self.defaultOn(f)
        }
    }

    /// The core's defaults (`settings.Default`), for a value the snapshot doesn't carry.
    private static func defaultOn(_ f: OnboardingFeature) -> Bool {
        f == .agents || f == .sessions || f == .canvas
    }

    /// The features whose setup shows under their switch on the setup page.
    public static func setupShown(chosen: [OnboardingFeature]) -> [OnboardingFeature] {
        chosen.filter(\.hasSetup)
    }

    public var page: OnboardingPage { Self.pages[index] }

    public var canGoBack: Bool { index > 0 }

    public var isLast: Bool { index == Self.pages.count - 1 }

    public mutating func next() {
        index = min(index + 1, Self.pages.count - 1)
    }

    public mutating func back() {
        index = max(index - 1, 0)
    }

    public mutating func restart(at page: OnboardingPage = .welcome) {
        index = Self.pages.firstIndex(of: page) ?? 0
    }

    /// The last page's button: a chosen setting that applies only after a restart restarts.
    public static func finishTitle(restartPending: Bool) -> String {
        restartPending ? "Finish and Restart" : "Finish"
    }
}

/// When the window opens by itself.
public enum OnboardingGate {
    /// UserDefaults: set once the window was finished or closed.
    public static let doneKey = "DeskPixieOnboardingDone"
    /// UserDefaults: the settings file a first launch found missing, until the window is done.
    /// The core writes that file on the same launch, so without it a first launch that never got
    /// to the window (a crash, a quit while starting) would leave the user looking like an old one.
    public static let pendingKey = "DeskPixieOnboardingPending"

    /// UserDefaults: the app version the window last opened for by itself, or was finished or
    /// closed in (#213).
    public static let permissionsVersionKey = "DeskPixiePermissionsCheckedVersion"

    /// For someone already onboarded: the window opens on its Permissions step when a permission
    /// is still not asked (`unasked`, of `AppPermission.launchCheck`), at most once per app
    /// version, so one turned off on purpose (or Accessibility, which reads the same) doesn't
    /// reopen it every launch (#213).
    public static func shouldOpenPermissions(done: Bool, version: String, checkedVersion: String?,
                                             unasked: [AppPermission]) -> Bool {
        done && version != checkedVersion && !unasked.isEmpty
    }

    /// For a new user (no settings file before this launch, or a first launch that never got to
    /// the window) who hasn't finished or closed it.
    public static func shouldOpenAtLaunch(done: Bool, settingsExisted: Bool, pending: Bool) -> Bool {
        !done && (pending || !settingsExisted)
    }

    /// Pending is kept per data folder (its settings file), so an isolated test copy
    /// (`DESKPIXIE_SUPPORT_DIR`) never makes the real app open the window.
    public static func isPending(stored: String?, settingsFile: URL) -> Bool {
        stored == settingsFile.path
    }

    public static func settingsFile(appSupport: URL) -> URL {
        appSupport.appendingPathComponent("settings.json")
    }
}

/// `setup.programs`: where each program was found, "" when it wasn't.
public struct SetupPrograms: Decodable, Equatable, Sendable {
    public let herdr: String
    public let tmux: String
    public let claude: String
    public let codex: String

    public init(herdr: String, tmux: String, claude: String, codex: String) {
        self.herdr = herdr
        self.tmux = tmux
        self.claude = claude
        self.codex = codex
    }
}

/// Where a permission stands. Accessibility can't tell not asked from turned off, and an app that
/// isn't running can't be asked about: both read as notDetermined.
public enum PermissionAccess: Equatable, Sendable {
    case notDetermined, granted, denied
}

/// An app DeskPixie sends Apple Events to; macOS asks once per app.
public enum AutomationTarget: String, CaseIterable, Sendable {
    case systemEvents = "com.apple.systemevents"
    case iTerm = "com.googlecode.iterm2"
    case music = "com.apple.Music"
    case spotify = "com.spotify.client"

    public var bundleID: String { rawValue }

    public var name: String {
        switch self {
        case .systemEvents: return "System Events"
        case .iTerm: return "iTerm"
        case .music: return "Music"
        case .spotify: return "Spotify"
        }
    }
}

/// A macOS permission DeskPixie uses, asked for together on the permissions step (#210) instead
/// of in the middle of whatever needs it first.
public enum AppPermission: Hashable, Sendable {
    case microphone, notifications, accessibility
    case automation(AutomationTarget)

    public var title: String {
        switch self {
        case .microphone: return "Microphone"
        case .notifications: return "Notifications"
        case .accessibility: return "Accessibility"
        case .automation(let app): return "Control \(app.name)"
        }
    }

    /// What DeskPixie needs it for.
    public var purpose: String {
        switch self {
        case .microphone: return "To hear you when you talk to Pixie."
        case .notifications: return "To tell you when a session or task finishes."
        case .accessibility: return "To bring a session's terminal window to the front."
        case .automation(.systemEvents): return "To find a session's terminal window."
        case .automation(.iTerm): return "To open and show sessions in iTerm."
        case .automation(.music), .automation(.spotify): return "To play, pause and skip when you ask."
        }
    }

    /// Its pane in System Settings ▸ Privacy & Security (Notifications has its own).
    public var settingsURL: URL {
        let pane: String
        switch self {
        case .microphone: pane = "com.apple.preference.security?Privacy_Microphone"
        case .notifications: pane = "com.apple.preference.notifications"
        case .accessibility: pane = "com.apple.preference.security?Privacy_Accessibility"
        case .automation: pane = "com.apple.preference.security?Privacy_Automation"
        }
        return URL(string: "x-apple.systempreferences:" + pane)!
    }

    /// Every permission, in the order the step shows them: Automation only for the apps installed
    /// (System Events always is).
    public static func all(installed: Set<AutomationTarget>) -> [AppPermission] {
        [.microphone, .notifications, .accessibility]
            + AutomationTarget.allCases.filter { $0 == .systemEvents || installed.contains($0) }.map(AppPermission.automation)
    }

    /// What the launch check reads (#213): the ones whose state can be read without asking.
    /// Automation only for apps that are running: one that isn't reads as not asked whatever was
    /// answered.
    public static func launchCheck(running: Set<AutomationTarget>) -> [AppPermission] {
        [.microphone, .notifications, .accessibility]
            + AutomationTarget.allCases.filter(running.contains).map(AppPermission.automation)
    }

    /// What Allow All asks for: every one not answered yet, Accessibility last (it sends the
    /// person to System Settings, which would cover the other dialogs). Accessibility never reads
    /// as answered, so once it was asked (`askedAccessibility`) it is left to System Settings.
    public static func toAsk(_ access: [(AppPermission, PermissionAccess)], askedAccessibility: Bool) -> [AppPermission] {
        let open = access.filter { $0.1 == .notDetermined }.map(\.0)
        return open.filter { $0 != .accessibility } + (askedAccessibility ? [] : open.filter { $0 == .accessibility })
    }

    /// A read merged with this window's answers: an Automation target quit after its question
    /// reads as notDetermined again, so the answer it gave stands.
    public static func merged(read: PermissionAccess, answered: PermissionAccess?) -> PermissionAccess {
        read == .notDetermined ? answered ?? read : read
    }
}

/// One line of a setup page: what is checked, whether it is ready, and the fix if there is one.
public struct OnboardingCheck: Equatable, Sendable {
    public enum Fix: Equatable, Sendable {
        /// Ask macOS for it (its own dialog).
        case allow(AppPermission)
        /// Already answered: only System Settings can change it.
        case openSettings(AppPermission)
        case installPix
        /// Creates the missing projects folder (#216).
        case createProjectsFolder
        /// Something to install: opens the page in the browser.
        case link(String, URL)
    }

    public let title: String
    public let ok: Bool
    public let detail: String
    public let fix: Fix?

    public init(title: String, ok: Bool, detail: String, fix: Fix? = nil) {
        self.title = title
        self.ok = ok
        self.detail = detail
        self.fix = fix
    }
}

/// A Done line.
public struct OnboardingSummaryLine: Equatable, Sendable {
    public let feature: OnboardingFeature
    public let ok: Bool
    public let detail: String
}

public enum OnboardingChecks {
    /// The key the voice service needs.
    public static func secretID(forProvider provider: String?) -> SettingsSecretID {
        provider == "gemini-live" ? .gemini : .openAI
    }

    public static func voiceKey(provider: String?, secrets: SettingsSecrets?) -> OnboardingCheck {
        let id = secretID(forProvider: provider)
        switch secrets?[id] {
        case .found?:
            return OnboardingCheck(title: id.variable, ok: true, detail: SettingsLayout.secretText(secrets, id))
        case .missing?:
            return OnboardingCheck(title: id.variable, ok: false,
                                   detail: "Paste your key to keep it in the Keychain, or set \(id.variable) in a .env file.")
        case nil:
            return OnboardingCheck(title: id.variable, ok: false, detail: "Couldn't read whether a key is set.")
        }
    }

    public static func microphone(_ access: PermissionAccess) -> OnboardingCheck {
        permission(.microphone, access, asked: false)
    }

    /// A permission's line. Accessibility has no answer to read back, so once it was asked
    /// (`asked`) and still isn't on, its fix is System Settings, where it is turned on.
    public static func permission(_ p: AppPermission, _ access: PermissionAccess, asked: Bool) -> OnboardingCheck {
        switch access {
        case .granted:
            return OnboardingCheck(title: p.title, ok: true, detail: "Allowed")
        case .notDetermined where p == .accessibility && asked:
            return OnboardingCheck(title: p.title, ok: false,
                                   detail: "Not on yet. Open Settings and turn DeskPixie on in the list; if it already is, switch it off and on again.",
                                   fix: .openSettings(p))
        case .notDetermined:
            return OnboardingCheck(title: p.title, ok: false, detail: "Not asked yet. \(p.purpose)", fix: .allow(p))
        case .denied:
            let place = p == .notifications ? "System Settings ▸ Notifications" : "System Settings ▸ Privacy & Security"
            return OnboardingCheck(title: p.title, ok: false, detail: "Turned off for DeskPixie in \(place).",
                                   fix: .openSettings(p))
        }
    }

    /// The Done line for the permissions step.
    public static func permissionsSummary(_ checks: [OnboardingCheck]) -> OnboardingCheck {
        let todo = checks.filter { !$0.ok }.count
        return todo == 0
            ? OnboardingCheck(title: "Permissions", ok: true, detail: "All allowed")
            : OnboardingCheck(title: "Permissions", ok: false,
                              detail: todo == 1 ? "1 still to allow" : "\(todo) still to allow")
    }

    static let herdrPage = URL(string: "https://herdr.dev")!
    static let tmuxPage = URL(string: "https://github.com/tmux/tmux/wiki/Installing")!
    static let claudePage = URL(string: "https://docs.anthropic.com/en/docs/claude-code/setup")!
    static let codexPage = URL(string: "https://github.com/openai/codex")!

    /// The session host's name ("herdr" or "tmux").
    public static func sessionHostName(_ values: JSONValue) -> String {
        values.at("agent.sessionHost")?.stringValue == "tmux" ? "tmux" : "herdr"
    }

    /// The session host's program and the coordinator's. `programs` nil: not read (yet, or the
    /// read failed: `checkFailed`), so nothing is reported found and no fix is offered.
    public static func agents(values: JSONValue, programs: SetupPrograms?, home: String,
                              checkFailed: Bool = false) -> [OnboardingCheck] {
        let codex = values.at("agent.coordinatorKind")?.stringValue == "codex"
        let pending = checkFailed ? "Couldn't check. Try Check Again." : "Checking…"
        let host = sessionHostName(values) == "tmux"
            ? program("tmux", programs?.tmux, home, programs == nil ? nil : .link("Get tmux", tmuxPage), pending)
            : program("herdr", programs?.herdr, home, programs == nil ? nil : .link("Get herdr", herdrPage), pending)
        let coordinator = codex
            ? program("Codex", programs?.codex, home, programs == nil ? nil : .link("Get Codex", codexPage), pending)
            : program("Claude Code", programs?.claude, home, programs == nil ? nil : .link("Get Claude Code", claudePage), pending)
        return [host, coordinator]
    }

    private static func program(_ name: String, _ path: String?, _ home: String, _ fix: OnboardingCheck.Fix?,
                                _ pending: String) -> OnboardingCheck {
        guard let path else {
            return OnboardingCheck(title: name, ok: false, detail: pending)
        }
        guard !path.isEmpty else {
            return OnboardingCheck(title: name, ok: false, detail: "Not found. Install it, then Check Again.", fix: fix)
        }
        let shown = !home.isEmpty && path.hasPrefix(home + "/") ? "~" + path.dropFirst(home.count) : path
        return OnboardingCheck(title: name, ok: true, detail: "Found at \(shown)")
    }

    /// The projects folder workers use (#216). Missing is created when DeskPixie starts, so it
    /// offers Create now; one that can't be used says so (Settings changes it). nil: not read.
    public static func projectsFolder(_ status: ProjectsFolderStatus?, home: String) -> OnboardingCheck {
        let title = "Projects folder"
        guard let status else { return OnboardingCheck(title: title, ok: false, detail: "Checking…") }
        let shown = ProjectsFolderStatus.shown(status.path, home: home)
        switch status.state {
        case .ready:
            return OnboardingCheck(title: title, ok: true, detail: shown)
        case .missing:
            return OnboardingCheck(title: title, ok: false, detail: "\(shown): \(status.detail)", fix: .createProjectsFolder)
        case .unusable:
            return OnboardingCheck(title: title, ok: false,
                                   detail: shown.isEmpty ? status.detail : "\(shown): \(status.detail)")
        }
    }

    /// The core's tray lines about the coordinator (herdr version, integration hook, …).
    public static func coordinatorLines(_ statusLines: [String]) -> [String] {
        statusLines.filter { $0.hasPrefix("Coordinator:") }
    }

    public static func pix(state: PixLinkState, onPath: Bool) -> OnboardingCheck {
        switch state {
        case .installed where onPath:
            return OnboardingCheck(title: "pix command", ok: true, detail: "Installed at ~/.local/bin/pix")
        case .installed:
            return OnboardingCheck(title: "pix command", ok: false,
                                   detail: "Installed, but ~/.local/bin isn't on your PATH yet. Add the line below to ~/.zshrc.")
        case .notInstalled:
            return OnboardingCheck(title: "pix command", ok: false, detail: "Not installed", fix: .installPix)
        case .broken:
            return OnboardingCheck(title: "pix command", ok: false, detail: "The link is broken (the app moved).", fix: .installPix)
        case .other(let what):
            return OnboardingCheck(title: "pix command", ok: false, detail: "~/.local/bin/pix is \(what); DeskPixie leaves it alone.")
        }
    }

    /// A feature card's status: "Ready", or how many checks are still to do; nil with nothing to check.
    public static func status(_ checks: [OnboardingCheck]) -> String? {
        guard !checks.isEmpty else { return nil }
        let todo = checks.filter { !$0.ok }.count
        return todo == 0 ? "Ready" : "\(todo) to do"
    }

    /// The voice hotkey as keycaps ("⌃", "⌥", "Space"); none set: the core's default.
    public static func hotkeyCaps(_ combo: String?) -> [String] {
        let combo = (combo?.isEmpty ?? true) ? LaunchSettings.defaultHotkey : combo!
        let text = SettingsLayout.formatHotkey(combo, platform: .macos)
        let modifiers = text.prefix { "⌃⌥⇧⌘".contains($0) }
        return modifiers.map(String.init) + [String(text.dropFirst(modifiers.count))]
    }

    /// The commands that start an agent through pix, shown on the sessions card.
    public static let pixCommands = ["pix claude", "pix codex"]

    /// One line per chosen feature: its ready text, or the first check still missing.
    public static func summary(chosen: [OnboardingFeature], checks: [OnboardingFeature: [OnboardingCheck]]) -> [OnboardingSummaryLine] {
        chosen.map { f in
            if let missing = checks[f]?.first(where: { !$0.ok }) {
                return OnboardingSummaryLine(feature: f, ok: false, detail: "\(missing.title): \(missing.detail)")
            }
            return OnboardingSummaryLine(feature: f, ok: true, detail: f.readyText)
        }
    }
}

/// Whether the app stays in the Dock when one of its windows closes: Settings and the first-run
/// window each make it a regular app while open, so closing one must not drop the other's Dock
/// icon (a minimized one still needs its tile). Other windows (the canvas) never show in the Dock.
public enum DockPresence {
    public static let windowIDs: Set<String> = ["settings", "onboarding"]

    public struct Window: Equatable, Sendable {
        public let id: String
        public let visible: Bool
        public let miniaturized: Bool

        public init(id: String, visible: Bool, miniaturized: Bool) {
            self.id = id
            self.visible = visible
            self.miniaturized = miniaturized
        }
    }

    public static func keepsDockIcon(closing: String, windows: [Window]) -> Bool {
        windows.contains { $0.id != closing && windowIDs.contains($0.id) && ($0.visible || $0.miniaturized) }
    }
}
