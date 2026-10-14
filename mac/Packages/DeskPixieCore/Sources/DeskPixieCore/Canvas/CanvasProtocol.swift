import Darwin
import Foundation

/// The canvas Content-Security-Policy and the meta-tag injection.
public enum CanvasCSP {
    /// Copy of Go's `canvas.CSP` (core/canvas/csp.go, the source of truth; a test keeps them
    /// equal). Used for responses Swift makes itself and when a resolve reply lacks the header.
    public static let policy = "default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'"

    /// Adds `<meta http-equiv="Content-Security-Policy">` (the same policy as the header, as a
    /// second layer). It goes after a leading doctype (past a UTF-8 BOM and whitespace), so the
    /// page does not drop into quirks mode; without a doctype it goes first (after a BOM).
    public static func withMetaCSP(_ body: Data, policy: String = CanvasCSP.policy) -> Data {
        let meta = Data("<meta http-equiv=\"Content-Security-Policy\" content=\"\(policy)\">".utf8)
        let bytes = [UInt8](body)
        let bom = bytes.starts(with: [0xEF, 0xBB, 0xBF]) ? 3 : 0
        var lead = bom
        while lead < bytes.count, [0x20, 0x09, 0x0A, 0x0C, 0x0D].contains(bytes[lead]) { lead += 1 }
        var at = 0
        let head = bytes[lead..<min(lead + 9, bytes.count)]
        if String(decoding: head, as: UTF8.self).lowercased() == "<!doctype",
           let gt = bytes[lead...].firstIndex(of: UInt8(ascii: ">")) {
            at = gt + 1
        }
        if at == 0 { at = bom }
        var out = Data(capacity: bytes.count + meta.count)
        out.append(contentsOf: bytes[..<at])
        out.append(meta)
        out.append(contentsOf: bytes[at...])
        return out
    }
}

/// The `canvas.resolve` reply (Go `canvas.Response`).
public struct CanvasResolveResult: Codable, Equatable, Sendable {
    public var status: Int
    public var contentType: String
    public var file: String?
    public var headers: [String: String]

    public init(status: Int, contentType: String, file: String?, headers: [String: String]) {
        self.status = status
        self.contentType = contentType
        self.file = file
        self.headers = headers
    }
}

/// A finished response for the scheme handler.
public struct CanvasHTTPResponse: Equatable, Sendable {
    public var status: Int
    public var headers: [String: String]
    public var body: Data
}

/// The `canvas://` URL scheme (port of `src-tauri/src/canvas/protocol.rs`): serves `/ds/**` and
/// `/pages/<slug>.html` read-only, to the canvas webview only, with the security headers on
/// every response (errors included). Resolving is Go's (`canvas.resolve`); reading is here.
public enum CanvasProtocol {
    public static let scheme = "canvas"
    /// The canvas web view's label; the scheme handler refuses any other.
    public static let webviewLabel = "canvas"
    public static let emptyPageURL = "canvas://localhost/ds/empty.html"
    /// Go's `canvas.MaxPageBytes`, enforced again while reading so a page that grows after the
    /// resolver's size check cannot slip through.
    public static let maxPageBytes = 5 * 1024 * 1024

    public static let securityHeaders: [String: String] = [
        "Content-Security-Policy": CanvasCSP.policy,
        "X-Content-Type-Options": "nosniff",
        "Referrer-Policy": "no-referrer",
        "Cache-Control": "no-store",
    ]

    /// The raw, still percent-encoded path (`URL.path` would decode `%2e%2e`, which the Go
    /// resolver must see to refuse).
    public static func rawPath(of url: URL) -> String {
        URLComponents(url: url, resolvingAgainstBaseURL: false)?.percentEncodedPath ?? ""
    }

    /// Pure request → response. `resolve` is `canvas.resolve` (throws when the engine fails).
    public static func respond(label: String, method: String, url: URL,
                               resolve: (String) throws -> CanvasResolveResult) -> CanvasHTTPResponse {
        guard label == webviewLabel else {
            debug("refused \(url.absoluteString) for webview \"\(label)\"")
            return plain(403)
        }
        let method = method.uppercased()
        guard method == "GET" || method == "HEAD" else { return plain(405) }
        let path = rawPath(of: url)
        let resolved: CanvasResolveResult
        do {
            resolved = try resolve(path)
        } catch {
            debug("503 \(path): \(error)")
            return plain(503)
        }
        guard resolved.status == 200, let file = resolved.file else {
            debug("\(resolved.status) \(path)")
            return plain(resolved.status == 200 ? 404 : resolved.status, headers: resolved.headers)
        }
        let limit = path.hasPrefix("/pages/") ? maxPageBytes : Int.max
        var body: Data
        do {
            body = try readNoFollow(file, limit: limit)
        } catch {
            debug("404 \(path): cannot read: \(error)")
            return plain(404, headers: resolved.headers)
        }
        var headers = merged(resolved.headers)
        headers["Content-Type"] = resolved.contentType
        if resolved.contentType.hasPrefix("text/html") {
            body = CanvasCSP.withMetaCSP(body, policy: headers["Content-Security-Policy"] ?? CanvasCSP.policy)
        }
        return CanvasHTTPResponse(status: 200, headers: headers, body: method == "HEAD" ? Data() : body)
    }

    public enum ReadError: Error, Equatable {
        case open(Int32)
        case notRegular
        case tooLarge
        case read(Int32)
    }

    /// Reads a regular file without following a symlink in its last component (a page swapped
    /// for a link after resolving is never followed), refusing anything over `limit` bytes.
    public static func readNoFollow(_ path: String, limit: Int) throws -> Data {
        let fd = open(path, O_RDONLY | O_NOFOLLOW | O_CLOEXEC)
        guard fd >= 0 else { throw ReadError.open(errno) }
        defer { close(fd) }
        var st = stat()
        guard fstat(fd, &st) == 0, (st.st_mode & S_IFMT) == S_IFREG else { throw ReadError.notRegular }
        if Int64(st.st_size) > Int64(limit) { throw ReadError.tooLarge }
        var out = Data()
        var buf = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let n = buf.withUnsafeMutableBytes { read(fd, $0.baseAddress, $0.count) }
            if n < 0 {
                if errno == EINTR { continue }
                throw ReadError.read(errno)
            }
            if n == 0 { break }
            out.append(contentsOf: buf[0..<n])
            if out.count > limit { throw ReadError.tooLarge }
        }
        return out
    }

    private static func merged(_ headers: [String: String]) -> [String: String] {
        securityHeaders.merging(headers) { _, theirs in theirs }
    }

    private static func plain(_ status: Int, headers: [String: String] = [:]) -> CanvasHTTPResponse {
        var h = merged(headers)
        h["Content-Type"] = "text/plain; charset=utf-8"
        return CanvasHTTPResponse(status: status, headers: h, body: Data(reason(status).utf8))
    }

    static func reason(_ status: Int) -> String {
        switch status {
        case 403: "Forbidden"
        case 404: "Not Found"
        case 405: "Method Not Allowed"
        case 503: "Service Unavailable"
        default: "error"
        }
    }

    /// Protocol misses are logged only with `CANVAS_DEBUG=1` (Rust spec §7: debug level).
    private static func debug(_ line: String) {
        if ProcessInfo.processInfo.environment["CANVAS_DEBUG"] != nil {
            FileHandle.standardError.write(Data("canvas: \(line)\n".utf8))
        }
    }
}

/// Web view hardening shared with the app's canvas window.
public enum CanvasHardening {
    /// Runs before page scripts, in every frame: the CSP's `connect-src` does not cover WebRTC,
    /// and mic/camera access is not a CSP matter, so remove both APIs (the permission delegate
    /// and the WebKit preferences are the other layers). Verbatim from ai-mascot `NO_WEBRTC`.
    public static let noWebRTCScript = #"""
    (function () {
      var kill = function (o, n) { try { Object.defineProperty(o, n, { value: undefined, configurable: false, writable: false }); } catch (_) {} };
      ['RTCPeerConnection', 'webkitRTCPeerConnection', 'RTCDataChannel', 'RTCSessionDescription', 'RTCIceCandidate'].forEach(function (n) { kill(window, n); });
      try { Object.defineProperty(Navigator.prototype, 'mediaDevices', { get: function () { return undefined; }, configurable: false }); } catch (_) {}
      try { Object.defineProperty(Navigator.prototype, 'sendBeacon', { value: function () { return false; }, configurable: false, writable: false }); } catch (_) {}
      try {
        if (window.MediaDevices) {
          ['getUserMedia', 'getDisplayMedia', 'enumerateDevices'].forEach(function (m) {
            Object.defineProperty(MediaDevices.prototype, m, {
              value: function () { return Promise.reject(new DOMException('blocked', 'NotAllowedError')); },
              configurable: false, writable: false
            });
          });
        }
      } catch (_) {}
    })();
    """#
}
