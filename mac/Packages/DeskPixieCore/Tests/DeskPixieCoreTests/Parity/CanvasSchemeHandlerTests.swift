import WebKit
import XCTest
@testable import DeskPixieCore

/// V-040: the `canvas://` WKURLSchemeHandler driven with a fake WKURLSchemeTask and a fake
/// engine answering `canvas.resolve`, plus the WebRTC-removal user script.
@MainActor
final class CanvasSchemeHandlerTests: XCTestCase {
    private final class FakeTask: NSObject, WKURLSchemeTask {
        let request: URLRequest
        var response: HTTPURLResponse?
        var body = Data()
        var finished = false
        var error: Error?
        var done: XCTestExpectation?

        init(_ url: String, method: String = "GET") {
            var r = URLRequest(url: URL(string: url)!)
            r.httpMethod = method
            request = r
        }

        func didReceive(_ response: URLResponse) { self.response = response as? HTTPURLResponse }
        func didReceive(_ data: Data) { body.append(data) }
        func didFinish() { finished = true; done?.fulfill() }
        func didFailWithError(_ error: Error) { self.error = error; done?.fulfill() }
    }

    /// Answers `canvas.resolve` like Go: `/pages/<slug>.html` in `root`, else 404 with headers.
    private final class ResolveEngine: CoreEngine, @unchecked Sendable {
        let root: URL
        let lock = NSLock()
        var paths: [String] = []
        init(root: URL) { self.root = root }

        func call(_ method: String, _ args: any Encodable) throws -> Data {
            XCTAssertEqual(method, "canvas.resolve")
            let json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(args)) as! [String: String]
            let path = json["path"]!
            lock.withLock { paths.append(path) }
            let file = root.appendingPathComponent(String(path.dropFirst()))
            let found = path.hasPrefix("/pages/") && FileManager.default.fileExists(atPath: file.path)
            let r = CanvasResolveResult(status: found ? 200 : 404, contentType: "text/html; charset=utf-8",
                                        file: found ? file.path : nil,
                                        headers: ["Content-Security-Policy": CanvasCSP.policy, "X-Go": "1"])
            return try JSONEncoder().encode(r)
        }

        func configure(_ c: EngineConfig) throws {}
        func hotkey() {}
        func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool { true }
        func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) { (0, false) }
        func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {}
        func micFailed(_ reason: String) {}
        func pollEvent() -> EngineEvent? { nil }
        func packJSON() -> Data? { nil }
        func shutdown() {}
    }

    private var engine: ResolveEngine!
    private var handler: CanvasSchemeHandler!

    /// A pages folder with `report.html`, the fake engine and the handler under test.
    private func make() throws {
        let tmp = FileManager.default.temporaryDirectory.appendingPathComponent("scheme-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: tmp.appendingPathComponent("pages"), withIntermediateDirectories: true)
        try Data("<!DOCTYPE html><title>R</title>".utf8).write(to: tmp.appendingPathComponent("pages/report.html"))
        addTeardownBlock { try? FileManager.default.removeItem(at: tmp) }
        engine = ResolveEngine(root: tmp)
        handler = CanvasSchemeHandler(engine: engine)
    }

    private func run(_ task: FakeTask) async {
        let e = expectation(description: "task done")
        task.done = e
        handler.start(task)
        await fulfillment(of: [e], timeout: 5)
    }

    private func assertSecurityHeaders(_ r: HTTPURLResponse?, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertEqual(r?.value(forHTTPHeaderField: "Content-Security-Policy"), CanvasCSP.policy, file: file, line: line)
        XCTAssertEqual(r?.value(forHTTPHeaderField: "X-Content-Type-Options"), "nosniff", file: file, line: line)
        XCTAssertEqual(r?.value(forHTTPHeaderField: "Referrer-Policy"), "no-referrer", file: file, line: line)
        XCTAssertEqual(r?.value(forHTTPHeaderField: "Cache-Control"), "no-store", file: file, line: line)
    }

    func testGetServesThePageWithHeadersAndMetaCSPAfterTheDoctype() async throws {
        try! make()
        let t = FakeTask("canvas://localhost/pages/report.html")
        await run(t)
        XCTAssertTrue(t.finished)
        XCTAssertEqual(t.response?.statusCode, 200)
        assertSecurityHeaders(t.response)
        XCTAssertEqual(t.response?.value(forHTTPHeaderField: "Content-Type"), "text/html; charset=utf-8")
        XCTAssertEqual(t.response?.value(forHTTPHeaderField: "Content-Length"), String(t.body.count))
        let html = String(decoding: t.body, as: UTF8.self)
        XCTAssertTrue(html.hasPrefix("<!DOCTYPE html><meta http-equiv=\"Content-Security-Policy\" content=\""), html)
        XCTAssertTrue(html.hasSuffix("<title>R</title>"))
        XCTAssertEqual(engine.paths, ["/pages/report.html"])
    }

    func testHeadSendsHeadersWithoutABody() async {
        try! make()
        let t = FakeTask("canvas://localhost/pages/report.html", method: "HEAD")
        await run(t)
        XCTAssertEqual(t.response?.statusCode, 200)
        assertSecurityHeaders(t.response)
        XCTAssertTrue(t.body.isEmpty)
        XCTAssertEqual(t.response?.value(forHTTPHeaderField: "Content-Length"), "0")
    }

    func testOtherMethodsAre405WithoutAskingTheCore() async {
        try! make()
        let t = FakeTask("canvas://localhost/pages/report.html", method: "POST")
        await run(t)
        XCTAssertEqual(t.response?.statusCode, 405)
        assertSecurityHeaders(t.response)
        XCTAssertTrue(engine.paths.isEmpty)
    }

    func testCoreMissesPassThroughAs404WithItsHeaders() async {
        try! make()
        let t = FakeTask("canvas://localhost/pages/%2e%2e/secret.html")
        await run(t)
        XCTAssertEqual(t.response?.statusCode, 404)
        assertSecurityHeaders(t.response)
        XCTAssertEqual(t.response?.value(forHTTPHeaderField: "X-Go"), "1")
        XCTAssertEqual(t.response?.value(forHTTPHeaderField: "Content-Type"), "text/plain; charset=utf-8")
        XCTAssertEqual(String(decoding: t.body, as: UTF8.self), "Not Found")
        XCTAssertEqual(engine.paths, ["/pages/%2e%2e/secret.html"], "the raw path goes to the resolver")
    }

    func testAStoppedTaskIsNeverCalledBack() async throws {
        try! make()
        let t = FakeTask("canvas://localhost/pages/report.html")
        handler.start(t)
        handler.stop(t)
        XCTAssertEqual(handler.liveCount, 0)
        try await Task.sleep(nanoseconds: 200_000_000)
        XCTAssertNil(t.response)
        XCTAssertFalse(t.finished)
    }

    func testTheWebRTCRemovalScript() {
        let s = CanvasHardening.noWebRTCScript
        for name in ["RTCPeerConnection", "webkitRTCPeerConnection", "RTCDataChannel", "RTCSessionDescription",
                     "RTCIceCandidate", "mediaDevices", "sendBeacon", "getUserMedia", "getDisplayMedia",
                     "enumerateDevices", "NotAllowedError"] {
            XCTAssertTrue(s.contains("'\(name)'"), name)
        }
        XCTAssertTrue(s.contains("configurable: false"), "page scripts cannot put them back")
        XCTAssertTrue(s.trimmingCharacters(in: .whitespacesAndNewlines).hasPrefix("(function () {"))
        XCTAssertTrue(s.trimmingCharacters(in: .whitespacesAndNewlines).hasSuffix("})();"))
        let script = CanvasHardening.userScript()
        XCTAssertEqual(script.source, s)
        XCTAssertEqual(script.injectionTime, .atDocumentStart, "before any page script")
        XCTAssertFalse(script.isForMainFrameOnly, "every frame")
    }
}
