import AppKit
import DeskPixieCore
import Observation
import os
import SwiftUI

/// What the first-run window reads besides settings (#149): the programs the agents need, the
/// macOS permissions (#210) and the pix command. Settings themselves come from the shared
/// `SettingsModel`, so the window and Settings never disagree.
@MainActor
@Observable
final class OnboardingState {
    var flow = OnboardingFlow()
    /// nil until `setup.programs` answers.
    private(set) var programs: SetupPrograms?
    /// The last `setup.programs` call failed (shown instead of "Checking…").
    private(set) var programsFailed = false
    /// The permissions this Mac has a use for, in the order the step shows them.
    private(set) var permissions = AppPermission.all(installed: [])
    private(set) var access: [AppPermission: PermissionAccess] = [:]
    /// Accessibility was asked for in this window: its line then points to System Settings.
    private(set) var askedAccessibility = false
    /// Allow All (or one Allow) is going through macOS's dialogs.
    private(set) var asking = false
    /// The Automation answers given in this window (a quit app can't be read back).
    @ObservationIgnored private var answered: [AppPermission: PermissionAccess] = [:]
    /// Only the latest read lands: one started while a dialog was up finishes late.
    @ObservationIgnored private var readGeneration = 0
    /// nil until the login shell's PATH has been read (off main: up to 2 s).
    private(set) var pix: PixCommandModel?

    @ObservationIgnored private let engine: CoreEngine
    @ObservationIgnored private let worker: CoreWorker
    @ObservationIgnored private let statusLinesNow: () -> [String]
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "onboarding")

    init(engine: CoreEngine, worker: CoreWorker, statusLines: @escaping () -> [String]) {
        self.engine = engine
        self.worker = worker
        self.statusLinesNow = statusLines
    }

    var statusLines: [String] { statusLinesNow() }
    let home = FileManager.default.homeDirectoryForCurrentUser.path

    /// Where the core loads packs from (bundled, then the user's), for the Pixie previews.
    private let packRoots: [URL] = (EngineConfig.forBundle(
        resources: Bundle.main.resourceURL ?? Bundle.main.bundleURL,
        appSupport: AppSupportDirectory.resolve(home: FileManager.default.homeDirectoryForCurrentUser)
    ).packRoots ?? []).map { URL(fileURLWithPath: $0, isDirectory: true) }

    /// Pack `id`'s folder; nil when it can't be found (its card then shows the bundled bit).
    func packFolder(_ id: String) -> URL? {
        OnboardingPixies.folder(id: id, roots: packRoots) { FileManager.default.fileExists(atPath: $0.path) }
    }

    /// Reads everything again (the window opened, or Check Again).
    func refresh() {
        refreshPermissions()
        refreshPrograms()
        refreshPix()
    }

    func refreshPrograms() {
        let engine = engine
        programsFailed = false
        Task { @MainActor [weak self] in
            let found: SetupPrograms?? = await self?.worker.run {
                do {
                    return try engine.call("setup.programs", as: SetupPrograms.self)
                } catch {
                    Self.log.error("setup.programs: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            if let found = found ?? nil {
                self?.programs = found
            } else {
                self?.programsFailed = true
            }
        }
    }

    var mic: PermissionAccess { access[.microphone] ?? .notDetermined }

    func refreshPermissions() {
        readGeneration += 1
        let generation = readGeneration
        Task { @MainActor [weak self] in
            let installed = await Task.detached { SystemPermissions.installedTargets() }.value
            let all = AppPermission.all(installed: installed)
            var read: [AppPermission: PermissionAccess] = [:]
            for p in all { read[p] = await SystemPermissions.access(p) }
            guard let self, generation == self.readGeneration else { return }
            self.permissions = all
            self.access = Dictionary(uniqueKeysWithValues: all.map { p in
                (p, AppPermission.merged(read: read[p] ?? .notDetermined, answered: self.answered[p]))
            })
        }
    }

    func permissionChecks() -> [OnboardingCheck] {
        permissions.map { OnboardingChecks.permission($0, access[$0] ?? .notDetermined, asked: askedAccessibility) }
    }

    /// Allow All: macOS's dialog for each one not answered yet, one after another.
    func askAll() {
        ask(openPermissions)
    }

    /// The ones Allow All would ask for.
    var openPermissions: [AppPermission] {
        AppPermission.toAsk(permissions.map { ($0, access[$0] ?? .notDetermined) }, askedAccessibility: askedAccessibility)
    }

    func ask(_ list: [AppPermission]) {
        guard !asking, !list.isEmpty else { return }
        asking = true
        Task { @MainActor [weak self] in
            for p in list {
                if p == .accessibility { self?.askedAccessibility = true }
                if let answer = await SystemPermissions.ask(p) { self?.answered[p] = answer }
            }
            self?.asking = false
            self?.refreshPermissions()
        }
    }

    func openSettings(_ p: AppPermission) {
        NSWorkspace.shared.open(p.settingsURL)
    }

    func refreshPix() {
        if let pix {
            pix.refresh()
            return
        }
        Task { @MainActor [weak self] in
            let path = await Task.detached { PixInstaller.loginShellPath() }.value
            self?.pix = PixCommandModel(installer: .forThisApp(path: path))
        }
    }

    /// Each chosen feature's checks, for its page and the Done summary.
    func checks(_ feature: OnboardingFeature, settings: SettingsModel) -> [OnboardingCheck] {
        let values = settings.snapshot?.values ?? .null
        switch feature {
        case .voice:
            return [OnboardingChecks.voiceKey(provider: values.at("voice.provider")?.stringValue, secrets: settings.secrets),
                    OnboardingChecks.microphone(mic)]
        case .agents:
            return OnboardingChecks.agents(values: values, programs: programs, home: home, checkFailed: programsFailed)
                + (settings.workersOn ? [OnboardingChecks.projectsFolder(settings.projectsFolder, home: home)] : [])
        case .sessions:
            guard let pix else { return [OnboardingCheck(title: "pix command", ok: false, detail: "Checking…")] }
            return [OnboardingChecks.pix(state: pix.state, onPath: pix.onPath)]
        case .canvas:
            return []
        }
    }
}

/// The first-run window: an ordinary titled window (the app shows in the Dock while it is open,
/// like Settings). Closing it at any step counts as done.
@MainActor
final class OnboardingController {
    static let size = NSSize(width: 600, height: 640)

    private let state: OnboardingState
    private let settings: () -> SettingsModel
    private var window: NSWindow?
    private var tokens: [NSObjectProtocol] = []

    init(engine: CoreEngine, worker: CoreWorker, settings: @escaping () -> SettingsModel,
         statusLines: @escaping () -> [String]) {
        self.state = OnboardingState(engine: engine, worker: worker, statusLines: statusLines)
        self.settings = settings
    }

    /// Opens the window (or brings it forward); a new one starts on `page`.
    func show(at page: OnboardingPage = .welcome) {
        let model = settings()
        model.load()
        state.refresh()
        if let window {
            WindowFocus.bringForward(window, app: NSApp, showInDock: true)
            return
        }
        state.flow.restart(at: page)
        let view = OnboardingView(state: state, settings: model,
                                  onFinish: { [weak self] in self?.finish() },
                                  onClose: { [weak self] in self?.window?.close() })
        let window = NSWindow(contentRect: NSRect(origin: .zero, size: Self.size),
                              styleMask: [.titled, .closable, .fullSizeContentView], backing: .buffered, defer: false)
        window.title = "Welcome to DeskPixie"
        // The pages run up under a clear title bar (the title stays for the Window menu and
        // VoiceOver); dragging the background moves the window.
        window.titlebarAppearsTransparent = true
        window.titleVisibility = .hidden
        window.isMovableByWindowBackground = true
        window.identifier = NSUserInterfaceItemIdentifier("onboarding")
        window.isReleasedWhenClosed = false
        window.contentView = NSHostingView(rootView: view)
        window.collectionBehavior.insert([.moveToActiveSpace, .fullScreenAuxiliary])
        window.center()
        let center = NotificationCenter.default
        tokens = [
            center.addObserver(forName: NSWindow.willCloseNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.closed() }
            },
            // Back from System Settings or an editor: a microphone allowed, a key added to .env, pix
            // installed by hand all show at once (Settings reloads the same way).
            center.addObserver(forName: NSWindow.didBecomeKeyNotification, object: window, queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.becameKey() }
            },
        ]
        self.window = window
        WindowFocus.bringForward(window, app: NSApp, showInDock: true)
    }

    /// Finish: a chosen setting that applies only after a restart restarts the app (as the
    /// Settings Restart bar does); otherwise the window just closes. A restart that couldn't be
    /// asked for keeps the window open, its notice saying why.
    private func finish() {
        Self.markDone()
        let model = settings()
        if model.restartText != nil {
            model.restart()
            return
        }
        window?.close()
    }

    private func becameKey() {
        settings().focusGained()
        state.refreshPermissions()
        state.refreshPix()
    }

    private func closed() {
        Self.markDone()
        settings().commitPendingEdits()
        tokens.forEach(NotificationCenter.default.removeObserver)
        tokens = []
        window = nil
        AppDock.windowClosed("onboarding")
    }

    private static func markDone() {
        UserDefaults.standard.set(true, forKey: OnboardingGate.doneKey)
        // Its Permissions step was just there: the launch check waits for the next version.
        UserDefaults.standard.set(SystemPermissions.appVersion, forKey: OnboardingGate.permissionsVersionKey)
        UserDefaults.standard.removeObject(forKey: OnboardingGate.pendingKey)
    }
}

/// Back to a menu-bar app when the last window that shows in the Dock closes (Settings or the
/// first-run window, `DockPresence`).
@MainActor
enum AppDock {
    static func windowClosed(_ id: String) {
        let windows = NSApp.windows.compactMap { w in
            w.identifier.map { DockPresence.Window(id: $0.rawValue, visible: w.isVisible, miniaturized: w.isMiniaturized) }
        }
        if !DockPresence.keepsDockIcon(closing: id, windows: windows) { NSApp.setActivationPolicy(.accessory) }
    }
}
