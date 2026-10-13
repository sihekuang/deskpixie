import AppKit
import DeskPixieCore
import Foundation
import os

/// Wires the Go engine to the mascot, the tray, the hotkey and the audio I/O, and drains
/// engine events on the main actor every 30 ms (Howl's poll pattern).
@MainActor
final class EngineCoordinator {
    static let pollInterval: UInt64 = 30_000_000
    static let hotkeyLabel = "⌃⌥Space"

    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "coordinator")
    private static let metricsLog = Logger(subsystem: "com.sihekuang.deskpixie", category: "metrics")

    private let appState: AppState
    private let engine: CoreEngine
    private let voiceIO: VoiceIO
    private let ring: PlaybackRing
    private let micRing: SampleRing
    private let pump: AudioPump
    private let hotkey: CarbonHotkey

    private(set) var mascot: MascotController?
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
        self.hotkey = hotkey
    }

    // MARK: Lifecycle

    func start() {
        let key = EnvSecrets.openAIKey(env: ProcessInfo.processInfo.environment,
                                       dotenvURL: EnvSecrets.defaultDotenvURL)
        Self.log.notice("OPENAI_API_KEY \(key == nil ? "missing" : "present", privacy: .public)")

        let config = EngineConfig(apiKey: key ?? "", model: "gpt-realtime", voice: "marin", personaPath: nil,
                                  idleTimeoutSecs: 7, pack: "bit", packRoots: Self.packRoots(), noisyRoom: false)
        do {
            try engine.configure(config)
            configured = true
        } catch {
            Self.log.error("configure failed: \(String(describing: error), privacy: .public)")
            appState.engineError = "Engine error: \(Self.describe(error))"
            appState.mascotState = "error"
        }

        if configured {
            if let m = MascotController.load(engine: engine) {
                mascot = m
                m.show()
            } else {
                appState.engineError = "Could not load the mascot pack"
            }
        }

        do {
            try hotkey.start { [weak self] in self?.talk() }
        } catch {
            Self.log.error("hotkey unavailable: \(String(describing: error), privacy: .public)")
            appState.hotkeyError = "Hotkey unavailable: \(Self.hotkeyLabel)"
        }

        refreshTray()
        startPolling()
        if configured { startAudio(retryForHotkey: false) }
    }

    func shutdown() {
        pollTask?.cancel()
        audioTask?.cancel()
        hotkey.stop()
        pump.stop()
        voiceIO.stop()
        mascot?.stop()
        engine.shutdown()
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
        switch id {
        case TrayMenu.quitID:
            NSApp.terminate(nil)
        case TrayMenu.micModeID:
            MicModes.show()
        default:
            struct Action: Encodable { let id: String }
            do {
                _ = try engine.call("app.action", Action(id: id))
            } catch {
                Self.log.error("app.action \(id, privacy: .public): \(String(describing: error), privacy: .public)")
            }
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
            } catch {
                Self.log.error("audio start failed: \(String(describing: error), privacy: .public)")
                failure = "Audio start failed: \(Self.describe(error))"
            }
            self?.appState.audioError = failure
            if failure != nil { self?.audioState = .stopped }
            if retryForHotkey {
                engine.hotkey()
                if let failure { engine.micFailed(failure) }
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
        case .pack, .position, .hotkey, .devices, .notify, .canvasOpen, .settingsChanged:
            // Handled by later work packages (S2–S6); nothing emits these yet.
            Self.log.debug("unhandled event \(String(describing: ev), privacy: .public)")
        }
    }

    // MARK: Helpers

    /// Bundled packs first, then the user's (later roots override earlier ones).
    static func packRoots() -> [String] {
        var roots: [String] = []
        if let res = Bundle.main.resourceURL {
            roots.append(res.appendingPathComponent("mascots", isDirectory: true).path)
        }
        let user = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/com.sihekuang.deskpixie/mascots", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: user, withIntermediateDirectories: true)
        } catch {
            log.error("cannot create \(user.path, privacy: .public): \(String(describing: error), privacy: .public)")
        }
        roots.append(user.path)
        return roots
    }

    private static func describe(_ error: Error) -> String {
        switch error {
        case DeskPixieEngineError.configureFailed(let m), DeskPixieEngineError.invalidConfig(let m): return m
        default: return String(describing: error)
        }
    }
}
