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
    /// `dp_call`: runs a core method and returns its result JSON (the envelope's `ok`
    /// value). Throws `DeskPixieEngineError.callFailed` with the core's message.
    func call(_ method: String, _ args: any Encodable) throws -> Data
    func shutdown()
}

public extension CoreEngine {
    /// `call` with `{}` arguments.
    func call(_ method: String) throws -> Data { try call(method, EmptyArgs()) }

    /// `call`, decoding the result as `T`.
    func call<T: Decodable>(_ method: String, _ args: any Encodable = EmptyArgs(), as: T.Type) throws -> T {
        try JSONDecoder().decode(T.self, from: call(method, args))
    }
}

/// Encodes as `{}`.
public struct EmptyArgs: Encodable, Sendable {
    public init() {}
}

/// The `dp_call` reply envelope: `{"ok":<json>}` or `{"error":"<msg>"}`, with `"code":"refused"`
/// when the core refused, in words for you, rather than failed.
public enum CallEnvelope {
    /// Returns the `ok` value re-encoded as JSON, or throws `refused` or `callFailed`.
    public static func unwrap(_ data: Data) throws -> Data {
        guard let obj = try? JSONSerialization.jsonObject(with: data, options: [.fragmentsAllowed]),
              let dict = obj as? [String: Any] else {
            throw DeskPixieEngineError.callFailed("malformed reply: \(String(decoding: data, as: UTF8.self))")
        }
        if let msg = dict["error"] {
            if dict["code"] as? String == "refused" { throw DeskPixieEngineError.refused(msg as? String ?? "\(msg)") }
            throw DeskPixieEngineError.callFailed(msg as? String ?? "\(msg)")
        }
        guard let ok = dict["ok"] else {
            throw DeskPixieEngineError.callFailed("malformed reply: \(String(decoding: data, as: UTF8.self))")
        }
        return try JSONSerialization.data(withJSONObject: ok, options: [.fragmentsAllowed, .sortedKeys])
    }
}

public enum DeskPixieEngineError: Error, Equatable {
    case initFailed(Int)
    case notInitialized
    case invalidConfig(String)
    case configureFailed(String)
    case callFailed(String)
    /// The core refused the call, and says why in words to show as they are (`chat.send`'s
    /// one-recipient rule).
    case refused(String)
}
