import Foundation

/// Microphone capture + speaker playback at 24 kHz mono Int16. Real impl: `VPIOVoiceIO`.
public protocol VoiceIO: AnyObject, Sendable {
    /// Arms capture and playback: the render side reads `playback` once a unit runs (units
    /// start on demand). Throws `VoiceIOError.permissionDenied` only when mic access was
    /// denied before; a user never asked is asked when a conversation turns the mic on.
    /// `onMicSamples` is called on the real-time input thread with each captured buffer and
    /// the capture time of its newest sample in Unix nanoseconds; it must not allocate, lock
    /// or block (typically it writes into a `SampleRing`). Mic delivery starts disabled.
    func start(playback: PlaybackRing,
               onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) async throws
    /// A conversation's mic start (`true`, behind the permission prompt) or stop. The unit
    /// keeps running while the speaker plays, so echo cancellation stays converged.
    func setMicEnabled(_ on: Bool)
    /// The CoreAudio UIDs to use (nil or "" = the Mac's default), from the `devices` event;
    /// read when the next unit is built.
    func setPinned(input: String?, output: String?)
    /// Receives the current conversation's audio events (`.live` after a granted prompt,
    /// `.failed(...)`), on an arbitrary queue.
    func setEventHandler(_ handler: @escaping @Sendable (MicEvent) -> Void)
    func stop()
}

public enum VoiceIOError: Error, Equatable {
    case permissionDenied
    /// An AudioUnit call failed: the step that failed and its OSStatus.
    case audioUnit(step: String, status: Int32)
}
