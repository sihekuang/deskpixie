import Foundation

/// Decides when the default audio devices really changed under a running unit. Port of
/// ai-mascot `crates/audio-macos/src/device_watch.rs`.
///
/// Starting VoiceProcessingIO on AirPods switches them to their hands-free (HFP) profile, which
/// can reshuffle the default input/output devices for a moment. That is caused by us, not by
/// the user, and must not end the conversation as a device change. So: the devices seen while
/// the unit settles become the baseline, and a later difference must hold for two polls in a
/// row. Generic over the device snapshot, so it is tested without hardware. Times are
/// monotonic seconds.
public struct DeviceWatch<D: Equatable> {
    /// How long after the unit starts device changes are taken as its own doing.
    public static var settle: TimeInterval { 3 }

    private var baseline: D
    private let started: TimeInterval
    /// A difference seen on the previous poll, waiting for confirmation.
    private var candidate: D?

    /// `current`: the devices right after the unit started.
    public init(_ current: D, now: TimeInterval) {
        baseline = current
        started = now
    }

    /// One poll. True when the devices changed for real.
    public mutating func changed(_ current: D, now: TimeInterval) -> Bool {
        if now - started < Self.settle {
            baseline = current
            candidate = nil
            return false
        }
        if current == baseline {
            candidate = nil
            return false
        }
        if candidate == current { return true }
        candidate = current
        return false
    }
}

/// Decides when the devices under a running unit really changed. A pinned device that is
/// gone, or came back as a different `AudioDeviceID`, is lost to the unit: the ID is compared
/// with the one it was built with. Like `DeviceWatch`, that is ignored while the unit settles
/// and must hold for two polls in a row (AirPods switching to hands-free may blip for a poll).
/// A direction following the default goes through `DeviceWatch`.
public struct UnitWatch {
    /// The default-following directions' devices; a pinned direction reads as a constant nil.
    struct Defaults: Equatable {
        var input: UInt32?
        var output: UInt32?

        init(_ w: WatchedPair) {
            func part(_ d: WatchedDevice) -> UInt32? {
                if case .systemDefault(let id) = d { return id }
                return nil
            }
            input = part(w.input)
            output = part(w.output)
        }
    }

    /// A pinned direction's ID when it differs from the one the unit was built with
    /// (outer nil: no mismatch; inner nil: gone).
    struct Mismatch: Equatable {
        var input: UInt32??
        var output: UInt32??
        var isNone: Bool { input == nil && output == nil }
    }

    public let used: DevicePair
    private var defaults: DeviceWatch<Defaults>
    private let started: TimeInterval
    /// A pinned mismatch seen on the previous poll, waiting for confirmation.
    private var candidate: Mismatch?
    /// The last mismatch logged, so each one is logged once.
    private var logged: Mismatch?

    public init(used: DevicePair, current: WatchedPair, now: TimeInterval) {
        self.used = used
        defaults = DeviceWatch(Defaults(current), now: now)
        started = now
    }

    /// One poll. True when the unit's devices changed for real. `log` hears each pinned
    /// mismatch when it is first seen, settling or not.
    public mutating func changed(_ current: WatchedPair, now: TimeInterval, log: (String) -> Void) -> Bool {
        func mismatch(_ used: DeviceInUse, _ seen: WatchedDevice) -> UInt32?? {
            if case .pinned(_, let id) = used, case .pinned(let nowID) = seen, nowID != id { return .some(nowID) }
            return nil
        }
        let seen = Mismatch(input: mismatch(used.input, current.input), output: mismatch(used.output, current.output))
        let pinnedLost: Bool
        if seen.isNone {
            candidate = nil
            logged = nil
            pinnedLost = false
        } else {
            if logged != seen {
                logged = seen
                for (dir, u, nowID) in [(AudioDirection.input, used.input, seen.input),
                                        (AudioDirection.output, used.output, seen.output)] {
                    if case .pinned(let uid, let id) = u, let nowID {
                        log("pinned \(dir.rawValue) device \(uid): id \(id) → \(nowID.map(String.init) ?? "gone")")
                    }
                }
            }
            if now - started < DeviceWatch<Defaults>.settle {
                candidate = nil
                pinnedLost = false
            } else if candidate == seen {
                pinnedLost = true
            } else {
                candidate = seen
                pinnedLost = false
            }
        }
        // The default watcher polls every time, so its baseline keeps settling.
        let defaultsChanged = defaults.changed(Defaults(current), now: now)
        return pinnedLost || defaultsChanged
    }
}
