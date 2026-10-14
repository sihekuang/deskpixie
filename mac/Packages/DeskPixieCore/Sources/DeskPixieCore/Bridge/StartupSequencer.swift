import Foundation
import os

/// Where launch is. Until `ready`/`failed` the tray shows only a disabled status line,
/// Settings…/Mic Mode/Quit, and nothing calls the core from the main thread.
public enum StartupPhase: Equatable, Sendable {
    case starting
    /// Still configuring after `permissionHintAfter`: most likely a macOS privacy prompt (a
    /// Documents-folder `open()` blocks until the user answers) or a slow disk.
    case waitingForPermission
    /// Still configuring after `stalledAfter`.
    case stalled
    case ready
    case failed(String)

    public var isStarting: Bool {
        switch self {
        case .starting, .waitingForPermission, .stalled: true
        case .ready, .failed: false
        }
    }

    /// The tray's status row; nil once launch has finished (a failure shows as the engine error).
    public var statusLine: String? {
        switch self {
        case .starting: "Starting…"
        case .waitingForPermission: "Starting… waiting for macOS permission?"
        case .stalled: "Still starting… check System Settings ▸ Privacy & Security"
        case .ready, .failed: nil
        }
    }
}

/// What the off-main launch work read from the core.
public struct StartupResult: Sendable {
    /// Why `dp_configure` failed; nil when it worked.
    public var configureError: String?
    /// `settings.snapshot`, nil when unavailable.
    public var snapshot: Data?
    /// `app.state`, nil when unavailable.
    public var appState: TrayAppState?

    public init(configureError: String? = nil, snapshot: Data? = nil, appState: TrayAppState? = nil) {
        self.configureError = configureError
        self.snapshot = snapshot
        self.appState = appState
    }
}

/// Runs the core's launch (data migration, `dp_configure`, the first `settings.snapshot` and
/// `app.state`) off the main thread, so the menu bar item appears at once even when the Go core
/// blocks in `open()` on a TCC prompt. `phase` advances to a "waiting for permission" hint
/// after `permissionHintAfter` and to `stalled` after `stalledAfter`.
@MainActor
public final class StartupSequencer {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "startup")

    public private(set) var phase: StartupPhase = .starting
    /// Called on the main actor for every phase change (the tray re-renders from it).
    public var onPhase: ((StartupPhase) -> Void)?

    private let permissionHintAfter: Duration
    private let stalledAfter: Duration

    public init(permissionHintAfter: Duration = .seconds(5), stalledAfter: Duration = .seconds(10)) {
        self.permissionHintAfter = permissionHintAfter
        self.stalledAfter = stalledAfter
    }

    /// Migrates, configures and reads on a background thread, then returns on the main actor.
    public func run(engine: CoreEngine, config: @escaping @Sendable () -> EngineConfig,
                    migrate: @escaping @Sendable () -> Void) async -> StartupResult {
        set(.starting)
        let hint = permissionHintAfter, stall = stalledAfter
        let timer = Task { @MainActor [weak self] in
            do {
                try await Task.sleep(for: hint)
                self?.set(.waitingForPermission)
                Self.log.notice("core still configuring after \(String(describing: hint), privacy: .public)")
                try await Task.sleep(for: stall - hint)
                self?.set(.stalled)
                Self.log.error("core still configuring after \(String(describing: stall), privacy: .public)")
            } catch {} // cancelled: launch finished
        }
        let result = await Task.detached(priority: .userInitiated) {
            Self.launch(engine: engine, config: config, migrate: migrate)
        }.value
        timer.cancel()
        set(result.configureError.map(StartupPhase.failed) ?? .ready)
        return result
    }

    /// The blocking part. Never call on the main thread.
    nonisolated static func launch(engine: CoreEngine, config: () -> EngineConfig,
                                   migrate: () -> Void) -> StartupResult {
        migrate()
        do {
            try engine.configure(config())
        } catch {
            log.error("configure failed: \(String(describing: error), privacy: .public)")
            return StartupResult(configureError: describe(error))
        }
        var result = StartupResult()
        do {
            result.snapshot = try engine.call("settings.snapshot")
        } catch {
            log.error("settings.snapshot: \(String(describing: error), privacy: .public)")
        }
        do {
            result.appState = try engine.call("app.state", as: TrayAppState.self)
        } catch {
            log.error("app.state: \(String(describing: error), privacy: .public)")
        }
        return result
    }

    private func set(_ p: StartupPhase) {
        guard p != phase || p == .starting else { return }
        phase = p
        onPhase?(p)
    }

    nonisolated static func describe(_ error: Error) -> String {
        switch error {
        case DeskPixieEngineError.configureFailed(let m), DeskPixieEngineError.invalidConfig(let m): m
        default: String(describing: error)
        }
    }
}
