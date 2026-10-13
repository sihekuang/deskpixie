import AppKit
import DeskPixieCore
import Foundation
import os

/// Wires the Go engine to the mascot, the tray, the hotkey and the audio I/O, and drains
/// engine events on the main actor every 30 ms (Howl's poll pattern).
@MainActor
final class EngineCoordinator {
    static let pollInterval: UInt64 = 30_000_000
    static let missingKeyStatus = "OPENAI_API_KEY not set"

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
            appState.status = key == nil ? Self.missingKeyStatus : "Ready — press ⌃⌥Space"
        } catch {
            Self.log.error("configure failed: \(String(describing: error), privacy: .public)")
            appState.status = "Engine error: \(Self.describe(error))"
            appState.mascotState = "error"
        }

        if configured {
            if let m = MascotController.load(engine: engine) {
                mascot = m
                m.show()
            } else {
                appState.status = "Could not load Bit's mascot pack"
            }
        }

        do {
            try hotkey.start { [weak self] in self?.talk() }
        } catch {
            Self.log.error("hotkey unavailable: \(String(describing: error), privacy: .public)")
            appState.status = "⌃⌥Space unavailable (taken by another app)"
        }

        startPolling()
        if configured { startAudio() }
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

    /// Same call as the ⌃⌥Space hotkey and a double-click on Bit.
    func talk() {
        engine.hotkey()
    }

    /// Debug: State ▸ menu sets the sprite directly, bypassing the engine.
    func debugSetState(_ s: String) {
        mascot?.setState(s)
        appState.mascotState = s
    }

    // MARK: Audio

    private func startAudio() {
        let voiceIO = voiceIO, ring = ring, micRing = micRing, pump = pump, engine = engine
        audioTask = Task { @MainActor [weak self] in
            do {
                // Runs on the real-time input thread: ring write only.
                try await voiceIO.start(playback: ring) { samples, unixNanos in
                    micRing.write(samples, timestamp: unixNanos)
                }
                pump.start()
                Self.log.notice("audio started (mic gated off)")
            } catch VoiceIOError.permissionDenied {
                Self.log.error("microphone permission denied")
                engine.micFailed("Microphone access denied")
                self?.appState.status = "Microphone access denied"
            } catch {
                Self.log.error("audio start failed: \(String(describing: error), privacy: .public)")
                engine.micFailed("Audio start failed: \(Self.describe(error))")
                self?.appState.status = "Audio start failed"
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
            appState.status = text
        case .error(let text):
            Self.log.error("engine error: \(text, privacy: .public)")
            appState.status = text
        case .transcript(let role, let text):
            Self.log.info("\(role, privacy: .public): \(text, privacy: .private)")
        case .metrics(let json):
            Self.metricsLog.notice("\(json, privacy: .public)")
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
