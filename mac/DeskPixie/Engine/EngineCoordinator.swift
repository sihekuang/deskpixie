import AppKit
import DeskPixieCore
import Foundation
import os

/// Wires the Go engine to the mascot, the tray, the hotkey, the audio I/O, notifications,
/// the canvas window and the settings window, and drains engine events on the main actor
/// every 30 ms (Howl's poll pattern). Every event kind of the plan's shared contract is
/// handled in `handle(_:)`.
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
    private lazy var canvas = CanvasWindowController(engine: engine)

    private(set) var mascot: MascotController?
    /// The combo the Carbon hotkey is bound to right now (nil = none).
    var boundCombo: String? { hotkeys.boundCombo }
    /// Settings platform, for formatting combos in status lines.
    private var platform = SettingsPlatform.macos
    /// Re-sends the device menus on plug/unplug; kept for the app's lifetime.
    private var deviceObserver: AudioDeviceListObserver?
    private var pollTask: Task<Void, Never>?
    private var audioTask: Task<Void, Never>?
    private var configured = false

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
    }

    // MARK: Lifecycle

    func start() {
        do {
            try engine.configure(Self.engineConfig())
            configured = true
        } catch {
            Self.log.error("configure failed: \(String(describing: error), privacy: .public)")
            appState.engineError = "Engine error: \(Self.describe(error))"
            appState.mascotState = "error"
        }

        // One read for everything launch needs; defaults when the core does not serve it.
        let snapshot = readSnapshot()
        let launch = LaunchSettings.fromSnapshot(snapshot)
        platform = launch.platform
        hotkeys.platform = platform

        if configured {
            let mascotSettings = snapshot.flatMap(MascotSettings.fromSnapshot) ?? .defaults
            if let m = MascotController.load(engine: engine, settings: mascotSettings) {
                mascot = m
                m.onDoubleClick = { [weak self] in self?.talk() }
                m.show()
            } else {
                appState.engineError = "Could not load the mascot pack"
            }
        }

        // The stored combo (Rust binds config.hotkey at startup); a failure is a tray line.
        hotkeys.bindAtLaunch(launch.hotkey)
        appState.hotkeyError = hotkeys.unavailableLine

        wireAudio(pinnedInput: launch.inputDevice, pinnedOutput: launch.outputDevice)

        refreshTray()
        startPolling()
        if configured { startAudio(retryForHotkey: false) }
    }

    func shutdown() {
        pollTask?.cancel()
        audioTask?.cancel()
        deviceObserver?.cancel()
        deviceObserver = nil
        hotkeys.stop()
        pump.stop()
        voiceIO.stop()
        mascot?.stop()
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
        hotkeys.changed(combo) { failed in
            do {
                _ = try engine.call("settings.hotkeyFailed", HotkeyFailedArgs(combo: failed))
            } catch {
                Self.log.error("settings.hotkeyFailed: \(String(describing: error), privacy: .public)")
            }
        }
        appState.hotkeyError = hotkeys.unavailableLine
    }

    // MARK: Actions

    /// Same call as the ⌃⌥Space hotkey and a double-click on Bit. If audio failed to start
    /// (e.g. mic permission was denied at launch and granted since), retry it first, so the
    /// conversation does not open a session and wait out the mic timeout.
    func talk() {
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
        case .settings:
            break // MenuBarMenu opens the Settings scene
        case .core(let id, let focusCanvas):
            struct Action: Encodable { let id: String }
            // A canvas the user asked for comes forward; core-initiated opens never take focus.
            if focusCanvas { canvas.requestFocusOnNextShow() }
            do {
                _ = try engine.call("app.action", Action(id: id))
            } catch {
                Self.log.error("app.action \(id, privacy: .public): \(String(describing: error), privacy: .public)")
            }
            if id == "fade" { mascot?.apply(settings: readMascotSettings()) }
            refreshTray()
        }
    }

    /// Re-reads `app.state`; on failure the last good model stays.
    func refreshTray() {
        do {
            appState.core = try engine.call("app.state", as: TrayAppState.self)
        } catch {
            Self.log.error("app.state: \(String(describing: error), privacy: .public)")
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
    private func sendDeviceChoices() {
        for args in AudioDevices.settingsChoices(CoreAudioDevices()) {
            do {
                _ = try engine.call("settings.choices", args)
            } catch {
                Self.log.error("settings.choices \(args.field, privacy: .public): \(String(describing: error), privacy: .public)")
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
            mascot?.setState(s)
            appState.mascotState = s
        case .mouth(let level):
            mascot?.setMouth(level)
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
            mascot?.applyPack(p)
        case .position(let x, let y):
            mascot?.moveTo(x: x, y: y)
        case .settingsChanged:
            // The mascot follows proximityFade.{enabled,radius}; the open Settings window re-reads.
            mascot?.apply(settings: readMascotSettings())
            SettingsHub.shared?.refresh()
        case .hotkey(let combo):
            hotkeyChanged(combo)
        case .devices(let input, let output):
            voiceIO.setPinned(input: input, output: output)
        case .notify:
            notifier.handle(ev)
        case .canvasOpen(let url, let title):
            canvas.show(url: url, title: title)
        }
    }

    // MARK: Helpers

    /// `settings.snapshot`, or nil (logged) while the core does not serve it.
    private func readSnapshot() -> Data? {
        do {
            return try engine.call("settings.snapshot")
        } catch {
            Self.log.error("settings.snapshot: \(String(describing: error), privacy: .public)")
            return nil
        }
    }

    /// The mascot's settings (`position`, `scale`, `proximityFade`); config.rs defaults
    /// while the core does not serve them.
    private func readMascotSettings() -> MascotSettings {
        readSnapshot().flatMap(MascotSettings.fromSnapshot) ?? .defaults
    }

    /// The `dp_configure` payload: paths only (P14a). Creates the user's data and pack folders.
    static func engineConfig() -> EngineConfig {
        let support = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/com.sihekuang.deskpixie", isDirectory: true)
        let userPacks = support.appendingPathComponent("mascots", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: userPacks, withIntermediateDirectories: true)
        } catch {
            log.error("cannot create \(userPacks.path, privacy: .public): \(String(describing: error), privacy: .public)")
        }
        let resources = Bundle.main.resourceURL ?? Bundle.main.bundleURL
        return EngineConfig.forBundle(resources: resources, appSupport: support)
    }

    private static func describe(_ error: Error) -> String {
        switch error {
        case DeskPixieEngineError.configureFailed(let m), DeskPixieEngineError.invalidConfig(let m): return m
        default: return String(describing: error)
        }
    }
}
