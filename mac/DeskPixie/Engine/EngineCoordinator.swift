import AppKit
import DeskPixieCore
import Foundation
import os

/// Wires the Go engine to the pixie, the tray, the hotkey, the audio I/O, notifications,
/// the canvas window and the settings window, and drains engine events on the main actor
/// every 30 ms (Howl's poll pattern). Every event kind of the plan's shared contract is
/// handled in `handle(_:)`.
///
/// Threading: no core call that can touch the disk, the network or spawn a process runs on the
/// main thread. Launch (migration, `dp_configure`, first reads) runs in `StartupSequencer`;
/// `app.action`, `app.state`, `settings.snapshot`/`choices`/`hotkeyFailed` go through `worker`.
/// Only `dp_poll_event` (a non-blocking queue pop), `dp_hotkey` and `dp_pack_json` stay on main.
@MainActor
final class EngineCoordinator {
    static let pollInterval: UInt64 = 30_000_000

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "coordinator")
    private static let metricsLog = Logger(subsystem: "com.sihekuang.deskpixie", category: "metrics")

    private let appState: AppState
    private let engine: CoreEngine
    private let voiceIO: VoiceIO
    private let ring: PlaybackRing
    private let micRing: SampleRing
    private let pump: AudioPump
    /// Binding, rollback and the "Hotkey unavailable" line (DeskPixieCore, tested there).
    private var hotkeys: HotkeyBinder!
    private let notifier = Notifier()
    /// Walks the core's FocusPlan to show a terminal (#116); set in init, since its outcome
    /// closure needs self.
    private var terminalFocus: TerminalFocusExecutor!
    private lazy var canvas = CanvasWindowController(engine: engine, worker: worker)

    private(set) var pixie: PixieController?
    /// The updates bubble beside Pixie, and the presence that holds its pops (both once Pixie is up).
    private var bubble: UpdatesBubbleController?
    /// The agent roster chip under Pixie (#91).
    private var roster: AgentRosterController?
    private var presence: PresenceMonitor?
    /// The combo the Carbon hotkey is bound to right now (nil = none).
    var boundCombo: String? { hotkeys.boundCombo }
    /// Settings platform, for formatting combos in status lines.
    private var platform = SettingsPlatform.macos
    /// Re-sends the device menus on plug/unplug; kept for the app's lifetime.
    private var deviceObserver: AudioDeviceListObserver?
    private var pollTask: Task<Void, Never>?
    private var audioTask: Task<Void, Never>?
    private var configured = false
    /// True once launch work has returned (configured or failed).
    private var startupFinished = false
    private let startup = StartupSequencer()
    /// Serial background queue for core calls that may block.
    private let worker = CoreWorker()

    private enum AudioState { case stopped, starting, running }
    private var audioState = AudioState.stopped

    init(appState: AppState, engine: CoreEngine, voiceIO: VoiceIO, ring: PlaybackRing,
         micRing: SampleRing, pump: AudioPump, hotkey: CarbonHotkey) {
        self.appState = appState
        self.engine = engine
        self.voiceIO = voiceIO
        self.ring = ring
        self.micRing = micRing
        self.pump = pump
        hotkeys = HotkeyBinder(registrar: hotkey) { [weak self] in self?.talk() }
        terminalFocus = TerminalFocusExecutor(primitives: SystemTerminalFocusPrimitives()) { [weak self] success, detail in
            guard let self else { return }
            let engine = self.engine
            self.worker.async {
                do { _ = try engine.call("agent.reportFocusOutcome", ReportFocusOutcomeArgs(success: success, detail: detail)) }
                catch { Self.log.error("agent.reportFocusOutcome: \(String(describing: error), privacy: .public)") }
            }
        }
    }

    // MARK: Lifecycle

    /// Returns at once: the tray shows "Starting…" while the core starts in the background;
    /// the pixie, hotkey and audio come up in `finishStart` back on the main actor.
    func start() {
        startup.onPhase = { [weak self] phase in self?.appState.startup = phase }
        let engine = engine
        Task { @MainActor [weak self] in
            guard let startup = self?.startup else { return }
            let result = await startup.run(engine: engine, config: { Self.engineConfig() },
                                           migrate: { DataMigration.runIfNeeded() })
            self?.finishStart(result)
        }
    }

    private func finishStart(_ result: StartupResult) {
        if let error = result.configureError {
            appState.engineError = "Engine error: \(error)"
            appState.pixieState = "error"
        } else {
            configured = true
        }

        // One read for everything launch needs; defaults when the core does not serve it.
        let snapshot = result.snapshot
        let launch = LaunchSettings.fromSnapshot(snapshot)
        platform = launch.platform
        hotkeys.platform = platform

        if configured {
            pushKeychainKeys()
            let pixieSettings = snapshot.flatMap(PixieSettings.fromSnapshot) ?? .defaults
            if let m = PixieController.load(engine: engine, settings: pixieSettings) {
                pixie = m
                m.onDoubleClick = { [weak self] in self?.talk() }
                m.show()
                roster = AgentRosterController(engine: engine, worker: worker, pixie: m)
                startBubble(pixie: m, settings: snapshot.flatMap(BubbleSettingsValues.fromSnapshot) ?? .defaults)
            } else {
                appState.engineError = "Could not load the pixie pack"
            }
        }

        // The stored combo (Rust binds config.hotkey at startup); a failure is a tray line.
        hotkeys.bindAtLaunch(launch.hotkey)
        appState.hotkeyError = hotkeys.unavailableLine

        wireAudio(pinnedInput: launch.inputDevice, pinnedOutput: launch.outputDevice)

        if let core = result.appState { appState.core = core }
        startupFinished = true
        startPolling()
        if configured { startAudio(retryForHotkey: false) }
    }

    /// Hands the core the API keys kept in the Keychain (`secrets.keychain`), off main: a
    /// Keychain read can block on an access prompt. Queued before any conversation can start a
    /// worker call; the voice loop reads keys on each connect.
    private func pushKeychainKeys() {
        let client = EngineSettingsClient(engine: engine)
        worker.async {
            let values = KeychainSecrets().values(for: SettingsSecretID.allCases.map(\.variable))
            do {
                try client.setKeychain(values)
            } catch {
                Self.log.error("secrets.keychain: \(String(describing: error), privacy: .public)")
            }
        }
    }

    func shutdown() {
        pollTask?.cancel()
        audioTask?.cancel()
        deviceObserver?.cancel()
        deviceObserver = nil
        hotkeys.stop()
        pump.stop()
        voiceIO.stop()
        presence?.stop()
        bubble?.stop()
        roster?.stop()
        pixie?.stop()
        // Quitting while the core is still starting: dp_configure may be blocked on another
        // thread; tearing the core down under it is unsafe, and there is nothing to save.
        guard startupFinished else { return }
        // Stop taking core work and wait for what is queued (app.action, app.state, …), so no
        // handler runs during or after dp_destroy (e.g. an agent restart spawning a process).
        worker.shutdown()
        // Before the engine goes: the last move/resize must still reach `canvas.geometry`.
        canvas.flushGeometry()
        engine.shutdown()
    }

    // MARK: Hotkey

    /// Binds `combo` (a stored combo such as `ctrl+alt+Space`) as the talk hotkey; on failure
    /// `previous` (default: the combo bound now) is bound again. Returns whether `combo` is now
    /// bound. Used at launch, for the `hotkey` event and by the Settings window's recorder.
    @discardableResult
    func bindHotkey(_ combo: String, previous: String?) -> Bool {
        let ok = hotkeys.bind(combo, previous: previous)
        appState.hotkeyError = hotkeys.unavailableLine
        return ok
    }

    /// `hotkey` event: the core reloaded a hand-edited combo. If it cannot be bound the old
    /// one stays and the core is told (`settings.hotkeyFailed`), so it does not adopt it.
    private func hotkeyChanged(_ combo: String) {
        let engine = engine, worker = worker
        hotkeys.changed(combo) { failed in
            worker.async {
                do {
                    _ = try engine.call("settings.hotkeyFailed", HotkeyFailedArgs(combo: failed))
                } catch {
                    Self.log.error("settings.hotkeyFailed: \(String(describing: error), privacy: .public)")
                }
            }
        }
        appState.hotkeyError = hotkeys.unavailableLine
    }

    // MARK: Actions

    /// Same call as the ⌃⌥Space hotkey and a double-click on Pixie. If audio failed to start
    /// (e.g. mic permission was denied at launch and granted since), retry it first, so the
    /// conversation does not open a session and wait out the mic timeout.
    func talk() {
        guard startupFinished else { return }
        guard configured, audioState == .stopped else {
            engine.hotkey()
            return
        }
        startAudio(retryForHotkey: true)
    }

    /// A tray menu id. Quit and Mic Mode are handled here (Settings… by the menu view);
    /// every other id is the core's (`app.action`), after which the menu is re-read.
    func perform(_ id: String) {
        switch TrayMenu.route(id) {
        case .quit:
            NSApp.terminate(nil)
        case .micMode:
            MicModes.show(using: appState.micModes)
        case .copy(let text):
            // Terminal commands are only ever copied, never run.
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
        case .settings:
            break // MenuBarMenu opens the Settings scene
        case .core(let id, let focusCanvas):
            struct Action: Encodable { let id: String }
            // A canvas the user asked for comes forward; core-initiated opens never take focus.
            if focusCanvas { canvas.requestFocusOnNextShow() }
            // Off main: actions open Finder/Terminal, rescan packs, restart the agent, …
            let engine = engine, worker = worker
            Task { @MainActor [weak self] in
                await worker.run {
                    do {
                        _ = try engine.call("app.action", Action(id: id))
                    } catch {
                        Self.log.error("app.action \(id, privacy: .public): \(String(describing: error), privacy: .public)")
                    }
                }
                guard let self else { return }
                if id == "fade" { self.applyPixieSettings() }
                self.refreshTray()
            }
        }
    }

    /// Re-reads `app.state` off main; on failure the last good model stays.
    func refreshTray() {
        let engine = engine, worker = worker
        Task { @MainActor [weak self] in
            let state: TrayAppState?? = await worker.run {
                do {
                    return try engine.call("app.state", as: TrayAppState.self)
                } catch {
                    Self.log.error("app.state: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            if let state = state ?? nil { self?.appState.core = state }
        }
    }

    /// `restart` event: start a fresh copy once this process has exited (so the new one can
    /// take the hotkey and the audio device), then quit.
    private func relaunch() {
        let pid = ProcessInfo.processInfo.processIdentifier
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/sh")
        p.arguments = ["-c", "while kill -0 \(pid) 2>/dev/null; do sleep 0.1; done; exec /usr/bin/open \"$0\"",
                       Bundle.main.bundleURL.path]
        do {
            try p.run()
        } catch {
            Self.log.error("relaunch failed: \(String(describing: error), privacy: .public)")
            return
        }
        NSApp.terminate(nil)
    }

    // MARK: Audio

    /// Pins the saved devices, routes the voice unit's failures to the core, and keeps the
    /// Settings device menus current (S3 wiring).
    private func wireAudio(pinnedInput: String?, pinnedOutput: String?) {
        voiceIO.setPinned(input: pinnedInput, output: pinnedOutput)
        let engine = engine
        voiceIO.setEventHandler { event in
            switch event {
            case .live:
                break // the core sees the mic as live on the first frame
            case .failed(.permissionDenied):
                engine.micFailed("Microphone access denied")
            case .failed(.deviceChanged):
                do {
                    _ = try engine.call("audio.deviceChanged")
                } catch {
                    Self.log.error("audio.deviceChanged: \(String(describing: error), privacy: .public)")
                }
            case .failed(.other(let reason)):
                Self.reportAudioFailure(engine, reason)
            }
        }
        sendDeviceChoices()
        deviceObserver = AudioDeviceListObserver { [weak self] in
            Task { @MainActor in self?.sendDeviceChoices() }
        }
    }

    /// The Microphone and Speaker menus (`settings.choices`); Go adds "System Default".
    /// Enumerates devices and calls the core off main.
    private func sendDeviceChoices() {
        let engine = engine
        worker.async {
            for args in AudioDevices.settingsChoices(CoreAudioDevices()) {
                do {
                    _ = try engine.call("settings.choices", args)
                } catch {
                    Self.log.error("settings.choices \(args.field, privacy: .public): \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    /// A unit that could not be built is not a permission problem: `audio.failed {reason}`.
    /// Falls back to `micFailed` while the core lacks that method.
    nonisolated private static func reportAudioFailure(_ engine: CoreEngine, _ reason: String) {
        do {
            _ = try engine.call("audio.failed", AudioFailedArgs(reason: reason))
        } catch {
            log.error("audio.failed: \(String(describing: error), privacy: .public)")
            engine.micFailed(reason)
        }
    }

    /// Starts VoiceIO (mic gated off) and the pump.
    /// - At launch (`retryForHotkey == false`) a failure only goes to the tray: reporting it to
    ///   the engine would make it fail the next conversation even if a later retry succeeds.
    /// - From `talk()` the hotkey is sent after the attempt; on failure `micFailed` follows it,
    ///   so the engine fails that conversation at once (if the report arrives while it is
    ///   still idle, the engine remembers it and fails at mic start instead).
    private func startAudio(retryForHotkey: Bool) {
        audioState = .starting
        let voiceIO = voiceIO, ring = ring, micRing = micRing, pump = pump, engine = engine
        audioTask = Task { @MainActor [weak self] in
            var failure: String?
            var permission = false
            do {
                // Runs on the real-time input thread: ring write only.
                try await voiceIO.start(playback: ring) { samples, unixNanos in
                    micRing.write(samples, timestamp: unixNanos)
                }
                pump.start()
                self?.audioState = .running
                Self.log.notice("audio started (mic gated off)")
            } catch VoiceIOError.permissionDenied {
                Self.log.error("microphone permission denied")
                failure = "Microphone access denied"
                permission = true
            } catch {
                Self.log.error("audio start failed: \(String(describing: error), privacy: .public)")
                failure = "Audio start failed: \(Self.describe(error))"
            }
            self?.appState.audioError = failure
            if failure != nil { self?.audioState = .stopped }
            if retryForHotkey {
                engine.hotkey()
                if let failure {
                    if permission { engine.micFailed(failure) } else { Self.reportAudioFailure(engine, failure) }
                }
            }
        }
    }

    // MARK: Events

    private func startPolling() {
        pollTask?.cancel()
        pollTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                guard let self else { return }
                while let ev = self.engine.pollEvent() { self.handle(ev) }
                try? await Task.sleep(nanoseconds: Self.pollInterval)
            }
        }
    }

    private func handle(_ ev: EngineEvent) {
        switch ev {
        case .state(let s):
            pixie?.setState(s)
            appState.pixieState = s
        case .mouth(let level):
            pixie?.setMouth(level)
        case .mic(let start):
            voiceIO.setMicEnabled(start)
            Self.log.info("mic \(start ? "on" : "off", privacy: .public)")
        case .status(let text):
            // The tray line comes from app.state; the core follows this with a tray event.
            Self.log.info("status: \(text, privacy: .public)")
        case .error(let text):
            Self.log.error("engine error: \(text, privacy: .public)")
        case .transcript(let role, let text):
            Self.log.info("\(role, privacy: .public): \(text, privacy: .private)")
        case .metrics(let json):
            Self.metricsLog.notice("\(json, privacy: .public)")
        case .tray:
            refreshTray()
        case .restart:
            relaunch()
        case .pack(let p):
            pixie?.applyPack(p)
        case .position(let x, let y):
            pixie?.moveTo(x: x, y: y)
        case .settingsChanged:
            // The pixie follows proximityFade.{enabled,radius}; the open Settings window re-reads.
            applyPixieSettings()
            SettingsHub.shared?.refresh()
        case .hotkey(let combo):
            hotkeyChanged(combo)
        case .devices(let input, let output):
            voiceIO.setPinned(input: input, output: output)
        case .notify:
            notifier.handle(ev)
        case .canvasOpen(let url, let title):
            canvas.show(url: url, title: title)
        case .updates(let lines):
            bubble?.apply(lines: lines)
        case .agents(let view):
            roster?.apply(view: view)
        case .showPane(let r):
            bubble?.showPane(r)
        case .focusPlan(let plan):
            terminalFocus.execute(plan)
        }
    }

    // MARK: Helpers

    /// Reads the pixie's settings (`position`, `scale`, `proximityFade`) off main and applies
    /// them; config.rs defaults while the core does not serve them.
    private func applyPixieSettings() {
        let engine = engine, worker = worker
        Task { @MainActor [weak self] in
            let snapshot: Data?? = await worker.run {
                do {
                    return try engine.call("settings.snapshot")
                } catch {
                    Self.log.error("settings.snapshot: \(String(describing: error), privacy: .public)")
                    return nil
                }
            }
            // nil: the worker closed (quitting); leave the pixie alone.
            guard let snapshot else { return }
            self?.pixie?.apply(settings: snapshot.flatMap(PixieSettings.fromSnapshot) ?? .defaults)
            self?.applyBubbleSettings(snapshot.flatMap(BubbleSettingsValues.fromSnapshot) ?? .defaults)
        }
    }

    /// The updates bubble and presence (`notifications.bubble.*`, `notifications.awayAfterMins`).
    private func applyBubbleSettings(_ s: BubbleSettingsValues) {
        bubble?.apply(settings: s)
        presence?.awayAfterMins = s.awayAfterMins
    }

    /// Creates the updates bubble beside Pixie and the presence monitor. Away/back goes to the
    /// bubble (held pops) and to the core (`presence.set`: no chime, no conversation by Pixie).
    private func startBubble(pixie m: PixieController, settings: BubbleSettingsValues) {
        let b = UpdatesBubbleController(engine: engine, worker: worker, pixie: m)
        b.onTalk = { [weak self] in self?.talk() }
        bubble = b
        let p = PresenceMonitor()
        presence = p
        applyBubbleSettings(settings)
        let engine = engine, worker = worker
        p.onChange = { [weak self] away in
            self?.bubble?.presence(present: !away)
            worker.async {
                do {
                    _ = try engine.call("presence.set", PresenceArgs(away: away))
                } catch {
                    Self.log.error("presence.set: \(String(describing: error), privacy: .public)")
                }
            }
        }
    }

    /// The `dp_configure` payload: paths only (P14a). Creates the user's data folder; the core
    /// makes its `pixies/` pack folder (moving a pre-#68 `mascots/` there first, so it must not
    /// exist before the core starts). Runs off main (inside the startup work).
    nonisolated static func engineConfig() -> EngineConfig {
        let support = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/com.sihekuang.deskpixie", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: support, withIntermediateDirectories: true)
        } catch {
            log.error("cannot create \(support.path, privacy: .public): \(String(describing: error), privacy: .public)")
        }
        let resources = Bundle.main.resourceURL ?? Bundle.main.bundleURL
        return EngineConfig.forBundle(resources: resources, appSupport: support)
    }

    nonisolated private static func describe(_ error: Error) -> String {
        switch error {
        case DeskPixieEngineError.configureFailed(let m), DeskPixieEngineError.invalidConfig(let m): return m
        default: return String(describing: error)
        }
    }
}

/// `presence.set` arguments.
private struct PresenceArgs: Encodable, Sendable {
    let away: Bool
}

/// `agent.reportFocusOutcome` arguments: the shell finished executing a FocusPlan (#116).
private struct ReportFocusOutcomeArgs: Encodable, Sendable {
    let success: Bool
    let detail: String
}
