import Foundation

/// A built, started VoiceProcessingIO unit. `stop` stops and disposes it; no callback runs
/// after it returns.
protocol VoiceUnitHandle: AnyObject {
    func stop()
}

/// The VoiceProcessingIO unit's lifecycle, the decisions of ai-mascot
/// `crates/audio-macos/src/engine.rs` `control_loop` (its mic pumping lives in `AudioPump`):
///
/// - the unit is built on demand: when there is audio to play, or the mic was switched on;
/// - with the mic off it is built without one (speaker only), so playing a spoken update
///   doesn't turn the Mac's mic on (#451); the mic switching on swaps it for one with the mic,
///   keeping the queued audio, and a speaker-only unit ignores the mic's device;
/// - it is built on the saved devices (`pick`), retrying on the defaults (`buildWithFallback`);
/// - a failed build discards the queued audio, is reported, and is not retried for
///   `buildBackoff` (1 s); a mic start during the back-off hears that failure at once;
/// - every `devicePoll` (1 s) a `UnitWatch` checks the devices (3 s settle, 2 polls, pinned
///   device must keep its ID); a real change drops the unit, discards the queued audio, mutes
///   the mic and reports `.deviceChanged` (the conversation ends; it is not rebuilt until audio
///   is needed again);
/// - with the mic off and the speaker idle for `idleStop` (300 ms) the unit is stopped.
///
/// Not thread-safe: `VPIOVoiceIO` calls it on its control queue only; tests drive `tick` with
/// a fake clock (monotonic seconds).
final class VoiceUnitControl {
    static let idleStop: TimeInterval = 0.3
    static let devicePoll: TimeInterval = 1
    /// After a failed unit build, don't retry (or re-report) for this long.
    static let buildBackoff: TimeInterval = 1

    /// Builds a unit on the devices; `withMic` false builds it speaker only (no input).
    typealias Build = (_ chosen: DevicePair, _ withMic: Bool) throws -> (unit: VoiceUnitHandle, used: DevicePair)

    struct Hooks {
        /// The saved UIDs (Settings ▸ Voice ▸ Microphone / Speaker); nil or "" = System Default.
        var saved: () -> (input: String?, output: String?)
        var micOn: () -> Bool
        var speakerIdle: () -> Bool
        /// Drop everything queued for the speaker (no unit is reading it).
        var discardSpeaker: () -> Void
        /// Stop forwarding mic audio, keeping the conversation to report to.
        var mute: () -> Void
        /// To the current conversation, if any.
        var report: (AudioFailure) -> Void
        var log: (String) -> Void
        var runningChanged: (Bool) -> Void
    }

    private struct Running {
        let unit: VoiceUnitHandle
        /// Built with the mic (else speaker only).
        let withMic: Bool
        /// The mic and speaker this unit uses, as built, and whether they changed since.
        var watch: UnitWatch
        /// The mic's device as first watched: a speaker-only unit keeps reading this.
        let inputAtBuild: WatchedDevice
        var idleSince: TimeInterval?
        var lastPoll: TimeInterval
    }

    private let source: () -> AudioDeviceSource
    private let build: Build
    private let hooks: Hooks
    private var running: Running?
    private var lastBuildFailure: (at: TimeInterval, message: String)?
    /// Audio is wanted while no unit runs; retried once a back-off expires.
    private var pendingEnsure = false

    init(source: @escaping () -> AudioDeviceSource, build: @escaping Build, hooks: Hooks) {
        self.source = source
        self.build = build
        self.hooks = hooks
    }

    var isRunning: Bool { running != nil }

    /// Audio needs a unit (Rust `Ctl::Ensure`).
    func ensure() { pendingEnsure = true }

    /// The mic was switched on (Rust `Ctl::MicStarted`).
    func micStarted(now: TimeInterval) {
        pendingEnsure = true
        // A new conversation hasn't heard about a failure that happened during the back-off:
        // tell it now.
        if running == nil, let f = lastBuildFailure, now - f.at < Self.buildBackoff {
            hooks.report(.other(f.message))
            pendingEnsure = false  // the failure is reported; the next start retries
            hooks.discardSpeaker()  // so nothing queued waits for that retry
        }
    }

    /// One control-loop pass (every 20 ms in the app).
    func tick(now: TimeInterval) {
        if !hooks.speakerIdle() { pendingEnsure = true }  // Rust: every `play` sends Ensure
        if pendingEnsure && running == nil {
            let backingOff = lastBuildFailure.map { now - $0.at < Self.buildBackoff } ?? false
            if !backingOff {
                pendingEnsure = false
                start(now: now)
            }
        } else if running != nil {
            pendingEnsure = false
        }

        guard var r = running else { return }
        // The mic came on while a speaker-only unit plays: swap it for one with the mic. The
        // queued audio stays in the ring for the new unit.
        if hooks.micOn() && !r.withMic {
            hooks.log("mic on: rebuilding the voice unit with the mic")
            r.unit.stop()
            running = nil
            start(now: now, swapping: true)
            return
        }
        // Device changes: drop the unit and tell the conversation.
        if now - r.lastPoll >= Self.devicePoll {
            r.lastPoll = now
            var current = AudioDevices.watched(source(), r.watch.used)
            if !r.withMic { current.input = r.inputAtBuild }  // a speaker-only unit has no mic
            if r.watch.changed(current, now: now, log: hooks.log) {
                stopUnit(r.unit)
                hooks.discardSpeaker()  // queued for the old device; the unit that would play it is gone
                hooks.mute()
                hooks.report(.deviceChanged)
                return
            }
        }
        // Stop the unit once mic and speaker have both been idle for a moment.
        let idle = !hooks.micOn() && hooks.speakerIdle()
        if !idle {
            r.idleSince = nil
        } else if let since = r.idleSince {
            if now - since >= Self.idleStop {
                hooks.log("audio idle: stopping the voice unit")
                stopUnit(r.unit)
                return
            }
        } else {
            r.idleSince = now
        }
        running = r
    }

    /// Stops the unit, if any (the app is quitting or the VoiceIO stopped).
    func shutdown() {
        if let r = running { stopUnit(r.unit) }
        pendingEnsure = false
    }

    /// `swapping`: replacing a running unit, so the app isn't told it stopped and started.
    private func start(now: TimeInterval, swapping: Bool = false) {
        let src = source()
        let saved = hooks.saved()
        var chosen = DevicePair.systemDefaults
        for (dir, uid) in [(AudioDirection.input, saved.input), (.output, saved.output)] {
            chosen[dir] = AudioDevices.pick(src, dir, saved: uid)
            if let uid, !uid.isEmpty, chosen[dir] == .systemDefault {
                hooks.log("audio device \(uid) (\(dir.rawValue)) isn't connected; using the default")
            }
        }
        let withMic = hooks.micOn()
        do {
            // A pinned device the unit can't start on: retry on the defaults.
            let (unit, used) = try AudioDevices.buildWithFallback(chosen, log: hooks.log) { try build($0, withMic) }
            lastBuildFailure = nil
            let current = AudioDevices.watched(src, used)
            running = Running(unit: unit, withMic: withMic, watch: UnitWatch(used: used, current: current, now: now),
                              inputAtBuild: current.input, idleSince: nil, lastPoll: now)
            if !swapping { hooks.runningChanged(true) }
        } catch {
            if swapping { hooks.runningChanged(false) }
            hooks.discardSpeaker()  // no unit will play it
            let message = "cannot start audio: \(error)"
            lastBuildFailure = (now, message)
            hooks.log(message)
            hooks.report(.other(message))
        }
    }

    private func stopUnit(_ unit: VoiceUnitHandle) {
        unit.stop()
        running = nil
        hooks.runningChanged(false)
    }
}
