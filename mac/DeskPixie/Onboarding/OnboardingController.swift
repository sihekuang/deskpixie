import AppKit
import AVFoundation
import DeskPixieCore
import Observation
import os
import SwiftUI

/// What the first-run window reads besides settings (#149): the programs the agents need, the
/// microphone permission and the pix command. Settings themselves come from the shared
/// `SettingsModel`, so the window and Settings never disagree.
@MainActor
@Observable
final class OnboardingState {
    var flow = OnboardingFlow()
    /// nil until `setup.programs` answers.
    private(set) var programs: SetupPrograms?
    /// The last `setup.programs` call failed (shown instead of "Checking…").
    private(set) var programsFailed = false
    private(set) var mic = MicrophoneAccess.notDetermined
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

    /// Reads everything again (the window opened, or Check Again).
    func refresh() {
        refreshMic()
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

    func refreshMic() {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized: mic = .granted
        case .notDetermined: mic = .notDetermined
        default: mic = .denied
        }
    }

    func requestMic() {
        AVCaptureDevice.requestAccess(for: .audio) { _ in
            Task { @MainActor [weak self] in self?.refreshMic() }
        }
    }

    func openMicSettings() {
        if let url = URL(string: "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone") {
            NSWorkspace.shared.open(url)
        }
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

    func show() {
        let model = settings()
        model.load()
        state.refresh()
        if let window {
            WindowFocus.bringForward(window, app: NSApp, showInDock: true)
            return
        }
        state.flow.restart()
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
        state.refreshMic()
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
