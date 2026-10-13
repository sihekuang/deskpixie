import Foundation

/// Microphone capture + speaker playback at 24 kHz mono Int16. Real impl: `VPIOVoiceIO`.
public protocol VoiceIO: AnyObject, Sendable {
    /// Asks for mic permission (throws `VoiceIOError.permissionDenied`), then starts
    /// capture and playback. The render side reads `playback`. `onMicSamples` is called
    /// on the real-time input thread with each captured buffer and the capture time of
    /// its newest sample in Unix nanoseconds; it must not allocate, lock or block
    /// (typically it writes into a `SampleRing`). Mic delivery starts disabled.
    func start(playback: PlaybackRing,
               onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) async throws
    /// Gates `onMicSamples` (the unit keeps running so echo cancellation stays converged).
    func setMicEnabled(_ on: Bool)
    func stop()
}

public enum VoiceIOError: Error, Equatable {
    case permissionDenied
    /// An AudioUnit call failed: the step that failed and its OSStatus.
    case audioUnit(step: String, status: Int32)
}
