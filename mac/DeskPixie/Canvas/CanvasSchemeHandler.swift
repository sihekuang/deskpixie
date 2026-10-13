import DeskPixieCore
import Foundation
import WebKit
import os

/// `canvas://` for the canvas web view only: asks Go (`canvas.resolve`) where the file is, then
/// `CanvasProtocol.respond` reads it (no symlinks, 5 MB page cap), adds the meta CSP to HTML and
/// the security headers to every response. The work runs off the main thread; a task WebKit
/// has stopped is never called back.
@MainActor
final class CanvasSchemeHandler: NSObject, WKURLSchemeHandler {
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "canvas")

    private struct ResolveArgs: Encodable, Sendable { let path: String }

    private let engine: CoreEngine
    /// Live tasks by token (not by object identity, which a later task may reuse).
    private var live: [UUID: any WKURLSchemeTask] = [:]

    init(engine: CoreEngine) {
        self.engine = engine
    }

    func webView(_ webView: WKWebView, start urlSchemeTask: any WKURLSchemeTask) {
        guard let url = urlSchemeTask.request.url else {
            urlSchemeTask.didFailWithError(URLError(.badURL))
            return
        }
        let token = UUID()
        live[token] = urlSchemeTask
        let method = urlSchemeTask.request.httpMethod ?? "GET"
        let engine = self.engine
        Task.detached(priority: .userInitiated) {
            let response = CanvasProtocol.respond(label: CanvasProtocol.webviewLabel, method: method, url: url) {
                try engine.call("canvas.resolve", ResolveArgs(path: $0), as: CanvasResolveResult.self)
            }
            await self.finish(token, url: url, response)
        }
    }

    func webView(_ webView: WKWebView, stop urlSchemeTask: any WKURLSchemeTask) {
        for (token, task) in live where task === urlSchemeTask {
            live[token] = nil
        }
    }

    private func finish(_ token: UUID, url: URL, _ r: CanvasHTTPResponse) {
        guard let task = live.removeValue(forKey: token) else { return }
        var headers = r.headers
        headers["Content-Length"] = String(r.body.count)
        guard let response = HTTPURLResponse(url: url, statusCode: r.status, httpVersion: "HTTP/1.1",
                                             headerFields: headers) else {
            Self.log.error("cannot build a response for \(url.absoluteString, privacy: .public)")
            task.didFailWithError(URLError(.cannotParseResponse))
            return
        }
        task.didReceive(response)
        if !r.body.isEmpty { task.didReceive(r.body) }
        task.didFinish()
    }
}
