import Foundation

/// Boundary between Swift and the Go core. Real impl: `LibDeskPixieEngine`.
public protocol CoreEngine: Sendable {
    func configure(_ c: EngineConfig) throws
    func hotkey()
    /// Real-time safe. Returns false when the frame was dropped.
    func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool
    /// Fills `into` with playback PCM; `flush` asks the caller to drop its buffered audio.
    func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool)
    func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64)
    func micFailed(_ reason: String)
    func pollEvent() -> EngineEvent?
    func packJSON() -> Data?
    func shutdown()
}

public enum DeskPixieEngineError: Error, Equatable {
    case initFailed(Int)
    case notInitialized
    case invalidConfig(String)
    case configureFailed(String)
}
