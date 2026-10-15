import AVFoundation
import CDP
import Foundation

/// Switching the mic on and off, behind the OS microphone permission. Port of ai-mascot
/// `crates/audio-macos/src/mic.rs` + `permission.rs`.
///
/// macOS hands an app that has never been asked ("not determined") silent mic buffers rather
/// than an error, so a user who then denies access just sees Pixie hear nothing. So the switch
/// asks up front, before the mic goes on: it stays off until the user answers (`start` returns
/// `.awaitingPermission`); allowing sends `.live`, and a denial fails the conversation with
/// `.permissionDenied`. The Go runtime waits up to 60 s (`voice.MicPermissionWait`) for the
/// first mic frame. No hardware here: the OS sits behind `MicAuthorizer`.
public enum MicAccess: Sendable {
    case granted
    case denied
    /// The user has never been asked.
    case notDetermined
}

/// The OS microphone permission.
public protocol MicAuthorizer: Sendable {
    func status() -> MicAccess
    /// Asks the user (the system prompt). `done(granted)` is called once, on any thread.
    func request(_ done: @escaping @Sendable (Bool) -> Void)
}

public enum MicStart: Equatable, Sendable {
    case live
    case awaitingPermission
}

/// Why audio failed, as reported to the current conversation (Rust `AudioError`).
public enum AudioFailure: Error, Equatable, Sendable {
    case permissionDenied
    /// The mic or speaker under a running unit changed for real (`audio.deviceChanged`).
    case deviceChanged
    /// The unit could not be built ("cannot start audio: …").
    case other(String)
}

/// What the current conversation hears from the audio side (Rust `CaptureEvent` minus frames,
/// which go through `SampleRing` → `AudioPump`).
public enum MicEvent: Equatable, Sendable {
    /// The user allowed the mic and it is on.
    case live
    case failed(AudioFailure)
}

/// Atomic flags shared by the VoiceIO, its real-time callbacks and `AudioPump`.
public final class AudioGate: @unchecked Sendable {
    // Slots: 0 mic on, 1 unit running.
    let slots: UnsafeMutablePointer<Int64>

    public init() {
        slots = .allocate(capacity: 2)
        slots.initialize(repeating: 0, count: 2)
    }

    deinit { slots.deallocate() }

    /// The mic is on: captured audio goes to the conversation (read by the input callback).
    public var micOn: Bool {
        get { cdp_load(slots) != 0 }
        set { cdp_store(slots, newValue ? 1 : 0) }
    }

    /// A unit is running, so the playback ring has a reader.
    public var unitRunning: Bool {
        get { cdp_load(slots + 1) != 0 }
        set { cdp_store(slots + 1, newValue ? 1 : 0) }
    }

    /// For the real-time callback: the mic flag's slot.
    var micOnSlot: UnsafeMutablePointer<Int64> { slots }
}

/// Mic on/off and the current conversation's events.
public final class MicState: @unchecked Sendable {
    public let gate: AudioGate
    private let lock = NSLock()
    private var events: (@Sendable (MicEvent) -> Void)?
    /// Bumped by every start and stop, so a late permission answer can tell it is stale.
    private var epoch: UInt64 = 0

    public init(gate: AudioGate = AudioGate()) {
        self.gate = gate
    }

    /// Lock-free (the real-time input callback reads `gate.micOn`).
    public var isOn: Bool { gate.micOn }

    /// Stops forwarding mic audio but keeps the events sink (to report why).
    public func mute() { gate.micOn = false }

    /// Sends to the current conversation, if any.
    public func send(_ event: MicEvent) {
        lock.withLock { events }?(event)
    }

    fileprivate func begin(_ sink: @escaping @Sendable (MicEvent) -> Void) -> UInt64 {
        lock.withLock {
            epoch += 1
            events = sink
            return epoch
        }
    }

    fileprivate func end() {
        lock.withLock {
            epoch += 1
            events = nil
            gate.micOn = false
        }
    }

    /// Runs `body` with the sink when `epoch` is still current (under the lock, so a stop
    /// cannot slip in between the check and turning the mic on).
    fileprivate func ifCurrent(_ epoch: UInt64, _ body: (@Sendable (MicEvent) -> Void) -> Void) -> Bool {
        lock.withLock {
            guard self.epoch == epoch, let events else { return false }
            body(events)
            return true
        }
    }
}

public final class MicSwitch: @unchecked Sendable {
    private let state: MicState
    private let auth: MicAuthorizer
    /// Called once the mic is actually on (the audio control then starts the unit).
    private let onStarted: @Sendable () -> Void

    public init(state: MicState, auth: MicAuthorizer, onStarted: @escaping @Sendable () -> Void) {
        self.state = state
        self.auth = auth
        self.onStarted = onStarted
    }

    /// Starts the conversation's mic. Throws `AudioFailure.permissionDenied` when access was
    /// denied before; asks first when the user was never asked.
    public func start(events: @escaping @Sendable (MicEvent) -> Void) throws -> MicStart {
        let access = auth.status()
        if access == .denied { throw AudioFailure.permissionDenied }
        let epoch = state.begin(events)
        if access == .granted {
            state.gate.micOn = true
            onStarted()
            return .live
        }
        let state = state, onStarted = onStarted
        auth.request { granted in
            var started = false
            _ = state.ifCurrent(epoch) { tx in  // else: stopped (or restarted) while the prompt showed
                if granted {
                    state.gate.micOn = true
                    tx(.live)
                    started = true
                } else {
                    tx(.failed(.permissionDenied))
                }
            }
            if started { onStarted() }
        }
        return .awaitingPermission
    }

    public func stop() {
        state.end()
    }
}

/// `MicAuthorizer` on AVFoundation's capture authorization.
public struct AVMicAuthorizer: MicAuthorizer {
    public init() {}

    public func status() -> MicAccess {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .notDetermined: return .notDetermined
        case .denied, .restricted: return .denied
        default: return .granted
        }
    }

    public func request(_ done: @escaping @Sendable (Bool) -> Void) {
        AVCaptureDevice.requestAccess(for: .audio, completionHandler: done)
    }
}
