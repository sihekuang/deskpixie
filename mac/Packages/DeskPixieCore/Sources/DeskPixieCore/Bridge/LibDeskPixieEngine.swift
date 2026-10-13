import Foundation
import CDP

/// Thin wrapper over the libdeskpixie C ABI. The Go side serialises access,
/// so plain methods are safe to call from any thread.
public final class LibDeskPixieEngine: CoreEngine, @unchecked Sendable {
    public init() {}

    public func configure(_ c: EngineConfig) throws {
        let rc0 = dp_init()
        if rc0 != 0 { throw DeskPixieEngineError.initFailed(Int(rc0)) }
        let data = try JSONEncoder().encode(c)
        let json = String(decoding: data, as: UTF8.self)
        let rc = json.withCString { dp_configure(UnsafeMutablePointer(mutating: $0)) }
        switch rc {
        case 0: return
        case 1: throw DeskPixieEngineError.notInitialized
        case 2: throw DeskPixieEngineError.invalidConfig(lastError() ?? "invalid config JSON")
        default: throw DeskPixieEngineError.configureFailed(lastError() ?? "dp_configure rc=\(rc)")
        }
    }

    public func hotkey() { dp_hotkey() }

    public func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool {
        guard let base = s.baseAddress, s.count > 0 else { return true }
        return dp_push_audio(base, Int32(s.count), Int64(bitPattern: capturedAt)) == 0
    }

    public func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) {
        guard let base = into.baseAddress, into.count > 0 else { return (0, false) }
        var flush: Int32 = 0
        let n = dp_pull_audio(base, Int32(into.count), &flush)
        return (Int(n), flush != 0)
    }

    public func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {
        dp_report_playback(played, Int32(clamping: buffered), rms, underruns)
    }

    public func micFailed(_ reason: String) {
        reason.withCString { dp_mic_failed(UnsafeMutablePointer(mutating: $0)) }
    }

    public func pollEvent() -> EngineEvent? {
        guard let cstr = dp_poll_event() else { return nil }
        defer { dp_free_string(cstr) }
        return EngineEvent.decode(Data(String(cString: cstr).utf8))
    }

    public func packJSON() -> Data? {
        guard let cstr = dp_pack_json() else { return nil }
        defer { dp_free_string(cstr) }
        return Data(String(cString: cstr).utf8)
    }

    public func call(_ method: String, _ args: any Encodable) throws -> Data {
        let argsJSON = String(decoding: try JSONEncoder().encode(args), as: UTF8.self)
        let reply: Data? = method.withCString { m in
            argsJSON.withCString { a in
                guard let cstr = dp_call(UnsafeMutablePointer(mutating: m), UnsafeMutablePointer(mutating: a)) else {
                    return nil
                }
                defer { dp_free_string(cstr) }
                return Data(String(cString: cstr).utf8)
            }
        }
        guard let reply else { throw DeskPixieEngineError.callFailed("dp_call returned NULL") }
        return try CallEnvelope.unwrap(reply)
    }

    public func lastError() -> String? {
        guard let cstr = dp_last_error() else { return nil }
        defer { dp_free_string(cstr) }
        return String(cString: cstr)
    }

    public func shutdown() { dp_destroy() }
}
