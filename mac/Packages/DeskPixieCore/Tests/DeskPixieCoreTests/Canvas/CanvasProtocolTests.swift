import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `src-tauri/src/canvas/protocol.rs` tests (9 of 9).
///
/// In DeskPixie the resolver (path → file, traversal, page-name and size checks) is Go's
/// `canvas.resolve` (core/canvas, ported there with its own traversal tests). Here it is
/// `FakeResolver`, which answers like Go: exact `/ds/<file>` and `/pages/<slug>.html` names
/// only, anything else a 404 with the security headers. What these tests pin is the Swift half:
/// the raw (still percent-encoded) path goes to the resolver, the file is read without
/// following symlinks and with the 5 MB page cap, HTML gets the meta CSP, every response
/// (errors included) carries the security headers, GET/HEAD only, canvas webview only.
///
/// Not ported: mod.rs `install_guide_copies_the_guide_and_examples_and_overwrites` — copying
/// CANVAS.md and the examples into app data is Go's job (P14 composition), not the window's.
final class CanvasProtocolTests: XCTestCase {
    private var tmp: URL!

    override func setUpWithError() throws {
        tmp = FileManager.default.temporaryDirectory
            .appendingPathComponent("deskpixie-protocol-\(UUID().uuidString)")
        let fm = FileManager.default
        try fm.createDirectory(at: tmp.appendingPathComponent("ds"), withIntermediateDirectories: true)
        try fm.createDirectory(at: tmp.appendingPathComponent("pages"), withIntermediateDirectories: true)
        try Data(":root{}".utf8).write(to: tmp.appendingPathComponent("ds/tokens.css"))
        try Data("<title>R</title>".utf8).write(to: tmp.appendingPathComponent("pages/report.html"))
        try Data(repeating: UInt8(ascii: "x"), count: CanvasProtocol.maxPageBytes + 1)
            .write(to: tmp.appendingPathComponent("pages/big.html"))
        try Data("secret".utf8).write(to: tmp.appendingPathComponent("secret.txt"))
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: tmp)
    }

    /// Answers like Go's `Resolver.Resolve`.
    private final class FakeResolver: @unchecked Sendable {
        let root: URL
        var seen: [String] = []
        init(root: URL) { self.root = root }

        func resolve(_ path: String) throws -> CanvasResolveResult {
            seen.append(path)
            let notFound = CanvasResolveResult(status: 404, contentType: "text/plain; charset=utf-8",
                                               file: nil, headers: CanvasProtocol.securityHeaders)
            let parts = path.split(separator: "/", omittingEmptySubsequences: false)
            guard parts.count == 3, parts[0].isEmpty, ["ds", "pages"].contains(parts[1]),
                  !parts[2].contains("%"), !parts[2].hasPrefix(".") else { return notFound }
            let file = root.appendingPathComponent("\(parts[1])/\(parts[2])").path
            guard FileManager.default.fileExists(atPath: file) else { return notFound }
            let type = file.hasSuffix(".css") ? "text/css; charset=utf-8" : "text/html; charset=utf-8"
            return CanvasResolveResult(status: 200, contentType: type, file: file,
                                       headers: CanvasProtocol.securityHeaders)
        }
    }

    private func respond(_ path: String, method: String = "GET", label: String = CanvasProtocol.webviewLabel,
                         resolver: FakeResolver? = nil) -> CanvasHTTPResponse {
        let r = resolver ?? FakeResolver(root: tmp)
        return CanvasProtocol.respond(label: label, method: method, url: URL(string: "canvas://localhost\(path)")!,
                                      resolve: r.resolve)
    }

    private var meta: String { "<meta http-equiv=\"Content-Security-Policy\" content=\"\(CanvasCSP.policy)\">" }

    func testServesDesignSystemFilesWithCspAndMime() {
        let r = respond("/ds/tokens.css")
        XCTAssertEqual(r.status, 200)
        XCTAssertEqual(r.body, Data(":root{}".utf8))
        XCTAssertEqual(r.headers["Content-Type"], "text/css; charset=utf-8")
        XCTAssertEqual(r.headers["Content-Security-Policy"], CanvasCSP.policy)
        XCTAssertEqual(r.headers["X-Content-Type-Options"], "nosniff")
        XCTAssertEqual(r.headers["Cache-Control"], "no-store")
        XCTAssertEqual(r.headers["Referrer-Policy"], "no-referrer")
    }

    func testServesPagesAsHtml() {
        let r = respond("/pages/report.html")
        XCTAssertEqual(r.status, 200)
        XCTAssertEqual(r.headers["Content-Type"], "text/html; charset=utf-8")
    }

    func testTraversalIsA404ThatStillCarriesTheCsp() {
        for path in ["/ds/%2e%2e/secret.txt", "/ds/../secret.txt", "/pages/..%2fsecret.txt"] {
            let resolver = FakeResolver(root: tmp)
            let r = respond(path, resolver: resolver)
            XCTAssertEqual(r.status, 404, path)
            XCTAssertEqual(r.headers["Content-Security-Policy"], CanvasCSP.policy, path)
            XCTAssertFalse(String(decoding: r.body, as: UTF8.self).contains("secret"), path)
            // The resolver sees the raw path, so Go can refuse every spelling itself.
            XCTAssertEqual(resolver.seen, [path], path)
        }
    }

    func testHtmlGetsAMetaCspAfterTheDoctype() {
        func with(_ b: [UInt8]) -> [UInt8] { [UInt8](CanvasCSP.withMetaCSP(Data(b))) }
        XCTAssertEqual(with(Array("<!DOCTYPE html><html>".utf8)), Array("<!DOCTYPE html>\(meta)<html>".utf8))
        XCTAssertEqual(with(Array("<p>hi".utf8)), Array("\(meta)<p>hi".utf8))
        let bom: [UInt8] = [0xEF, 0xBB, 0xBF]
        XCTAssertEqual(with(bom + Array("<!doctype html><p>".utf8)), bom + Array("<!doctype html>\(meta)<p>".utf8),
                       "BOM before the doctype")
        XCTAssertEqual(with(bom + Array("<p>x".utf8)), bom + Array("\(meta)<p>x".utf8),
                       "BOM stays first without a doctype")
        let r = respond("/pages/report.html")
        XCTAssertTrue(String(decoding: r.body, as: UTF8.self)
            .hasPrefix("<meta http-equiv=\"Content-Security-Policy\""))
        XCTAssertEqual(respond("/ds/tokens.css").body, Data(":root{}".utf8), "only html is rewritten")
    }

    func testAPageSwappedForASymlinkIsNotFollowed() throws {
        try FileManager.default.createSymbolicLink(at: tmp.appendingPathComponent("pages/link.html"),
                                                   withDestinationURL: tmp.appendingPathComponent("secret.txt"))
        // The fake resolver hands the link back as if it had been swapped in after resolving.
        let r = respond("/pages/link.html")
        XCTAssertEqual(r.status, 404)
        XCTAssertFalse(String(decoding: r.body, as: UTF8.self).contains("secret"))
    }

    func testOversizedPageIsA404() {
        XCTAssertEqual(respond("/pages/big.html").status, 404)
    }

    func testOtherWebviewsAreRefused() {
        XCTAssertEqual(respond("/ds/tokens.css", label: "mascot").status, 403)
    }

    func testOnlyGetAndHeadAreAllowed() {
        XCTAssertEqual(respond("/ds/tokens.css", method: "POST").status, 405)
        let head = respond("/ds/tokens.css", method: "HEAD")
        XCTAssertEqual(head.status, 200)
        XCTAssertTrue(head.body.isEmpty)
    }

    func testUnknownAreasAre404() {
        XCTAssertEqual(respond("/app/index.html").status, 404)
    }

    // MARK: Swift-only

    func testAnEngineFailureIsA503WithTheSecurityHeaders() {
        struct Boom: Error {}
        let r = CanvasProtocol.respond(label: CanvasProtocol.webviewLabel, method: "GET",
                                       url: URL(string: "canvas://localhost/ds/tokens.css")!,
                                       resolve: { _ in throw Boom() })
        XCTAssertEqual(r.status, 503)
        XCTAssertEqual(r.headers["Content-Security-Policy"], CanvasCSP.policy)
        XCTAssertEqual(r.headers["Content-Type"], "text/plain; charset=utf-8")
    }

    func testDesignSystemFilesHaveNoSizeCap() {
        let big = tmp.appendingPathComponent("ds/big.css")
        XCTAssertNoThrow(try Data(repeating: 0x20, count: CanvasProtocol.maxPageBytes + 1).write(to: big))
        XCTAssertEqual(respond("/ds/big.css").status, 200)
    }

    func testResolveResultDecodesGoJson() throws {
        let json = #"{"status":200,"contentType":"text/css; charset=utf-8","file":"/x/tokens.css","headers":{"Cache-Control":"no-store"}}"#
        let r = try JSONDecoder().decode(CanvasResolveResult.self, from: Data(json.utf8))
        XCTAssertEqual(r, CanvasResolveResult(status: 200, contentType: "text/css; charset=utf-8",
                                              file: "/x/tokens.css", headers: ["Cache-Control": "no-store"]))
        let refused = #"{"status":404,"contentType":"text/plain; charset=utf-8","headers":{}}"#
        XCTAssertNil(try JSONDecoder().decode(CanvasResolveResult.self, from: Data(refused.utf8)).file)
    }

    /// The Swift copy of the policy (used for Swift-made errors and the meta tag fallback) must
    /// match Go's `canvas.CSP`, the single source of truth.
    func testCspMatchesTheGoConstant() throws {
        let go = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appendingPathComponent("core/canvas/csp.go")
        let src = try String(contentsOf: go, encoding: .utf8)
        let line = try XCTUnwrap(src.split(separator: "\n").first { $0.hasPrefix("const CSP = ") })
        XCTAssertEqual(String(line.dropFirst("const CSP = \"".count).dropLast()), CanvasCSP.policy)
    }

    func testNoWebRtcScriptRemovesTheApis() {
        for name in ["RTCPeerConnection", "webkitRTCPeerConnection", "RTCDataChannel", "mediaDevices",
                     "getUserMedia", "getDisplayMedia", "sendBeacon"] {
            XCTAssertTrue(CanvasHardening.noWebRTCScript.contains(name), name)
        }
    }
}
