import AppKit
import DeskPixieCore
import WebKit
import os

/// The canvas window (port of ai-mascot `src-tauri/src/canvas/window.rs`).
///
/// - `show(url:title:)` on the core's `canvas.open` event: builds the window (960×720 centred,
///   or the saved `canvas.window` geometry when it is still on a display) without activating
///   the app or taking key focus; when already open it navigates in place.
/// - The web view is locked down: `canvas://` only via `CanvasSchemeHandler`, WebRTC and
///   media devices removed by a document-start script in every frame (plus WebKit's private
///   preferences when present), media-capture permission denied, navigation pinned to the
///   current page (fragments allowed), no new windows, non-persistent data store.
/// - Geometry is saved via `canvas.geometry` once the window has been still for 500 ms, and on
///   close / app exit (`flushGeometry()`).
@MainActor
final class CanvasWindowController: NSObject, NSWindowDelegate, WKNavigationDelegate, WKUIDelegate {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "canvas")

    private let engine: CoreEngine
    /// Orders every `canvas.geometry` save on the core worker (see `flushGeometry`).
    private let saver: CanvasGeometrySaver
    private let navGuard = CanvasNavGuard()
    private var window: NSWindow?
    private var webView: WKWebView?
    private var currentURL: URL?
    /// Geometry waiting to be saved, and the debounce that saves it.
    private var pending: CanvasGeometry?
    private var settleTask: Task<Void, Never>?
    /// The last geometry sent, used when `settings.snapshot` is unavailable.
    private var lastSaved: CanvasGeometry?
    private var focusNextShow = false

    init(engine: CoreEngine, worker: CoreWorker) {
        self.engine = engine
        saver = CanvasGeometrySaver(worker: worker) { g in
            do {
                _ = try engine.call("canvas.geometry", g)
            } catch {
                Self.log.error("canvas.geometry: \(String(describing: error), privacy: .public)")
            }
        }
    }

    var isOpen: Bool { window != nil }

    // MARK: Showing

    /// `canvas.open {url, title}`. An empty url shows the empty state. Only `canvas://` URLs
    /// are accepted.
    func show(url: String, title: String) {
        let raw = url.isEmpty ? CanvasProtocol.emptyPageURL : url
        guard let target = URL(string: raw), target.scheme?.lowercased() == CanvasProtocol.scheme else {
            Self.log.error("refused to show \(url, privacy: .public): not a canvas:// URL")
            return
        }
        let windowTitle = CanvasWindowTitle.make(title)
        navGuard.allow(target)
        currentURL = target
        if let window, let webView {
            // Already open: navigate in place, focus stays where it is.
            window.title = windowTitle
            webView.load(URLRequest(url: target))
        } else {
            build(url: target, title: windowTitle)
        }
        if focusNextShow {
            focusNextShow = false
            focus()
        }
    }

    /// The next `show` comes from an explicit user action (tray ▸ Canvas): bring it forward.
    func requestFocusOnNextShow() {
        focusNextShow = true
    }

    /// Brings the canvas window forward. Only for explicit user actions.
    func focus() {
        guard let window else { return }
        WindowFocus.bringForward(window, app: NSApp, showInDock: false)
    }

    private func build(url: URL, title: String) {
        let config = WKWebViewConfiguration()
        config.setURLSchemeHandler(CanvasSchemeHandler(engine: engine), forURLScheme: CanvasProtocol.scheme)
        config.websiteDataStore = .nonPersistent()
        config.preferences.javaScriptCanOpenWindowsAutomatically = false
        config.userContentController.addUserScript(CanvasHardening.userScript())
        Self.disableWebRTC(config.preferences)

        let webView = WKWebView(frame: NSRect(origin: .zero, size: CanvasPlacement.defaultSize),
                                configuration: config)
        webView.navigationDelegate = self
        webView.uiDelegate = self

        let window = NSWindow(contentRect: NSRect(origin: .zero, size: CanvasPlacement.defaultSize),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.title = title
        window.contentMinSize = CanvasPlacement.minSize
        window.contentView = webView
        place(window)
        window.delegate = self
        self.window = window
        self.webView = webView
        webView.load(URLRequest(url: url))
        // orderFront, not makeKeyAndOrderFront, and no NSApp.activate: the user's focus stays.
        window.orderFront(nil)
    }

    /// Saved geometry if it is still on a display (clamped), else 960×720 centred.
    private func place(_ window: NSWindow) {
        guard let saved = savedGeometry(), let primary = NSScreen.screens.first,
              let g = CanvasPlacement.place(saved, areas: Self.workAreas()) else {
            window.setContentSize(CanvasPlacement.defaultSize)
            window.center()
            return
        }
        let frameSize = window.frameRect(forContentRect: NSRect(x: 0, y: 0, width: g.width, height: g.height)).size
        let origin = CanvasScreenSpace.appKitOrigin(topLeftX: g.x, topLeftY: g.y, frameHeight: frameSize.height,
                                                    primaryMaxY: primary.frame.maxY)
        window.setFrame(NSRect(origin: origin, size: frameSize), display: false)
    }

    /// `values.canvas.window` from `settings.snapshot`; falls back to what this process last
    /// saved (e.g. before the settings methods exist).
    private func savedGeometry() -> CanvasGeometry? {
        do {
            if let g = CanvasSettingsValues.window(fromSnapshot: try engine.call("settings.snapshot")) {
                return g
            }
        } catch {
            Self.log.notice("settings.snapshot unavailable: \(String(describing: error), privacy: .public)")
        }
        return lastSaved
    }

    private static func workAreas() -> [CanvasRect] {
        guard let primary = NSScreen.screens.first else { return [] }
        return NSScreen.screens.map { CanvasScreenSpace.topLeft($0.visibleFrame, primaryMaxY: primary.frame.maxY) }
    }

    /// WebKit's own WebRTC and media-device switches (private `WKPreferences` setters). Each
    /// selector is checked first; a missing one is logged, never a crash.
    private static func disableWebRTC(_ prefs: WKPreferences) {
        typealias SetBool = @convention(c) (AnyObject, Selector, Bool) -> Void
        for name in ["_setPeerConnectionEnabled:", "_setMediaDevicesEnabled:"] {
            let sel = NSSelectorFromString(name)
            guard prefs.responds(to: sel), let imp = prefs.method(for: sel) else {
                log.notice("WKPreferences has no \(name, privacy: .public)")
                continue
            }
            unsafeBitCast(imp, to: SetBool.self)(prefs, sel, false)
        }
    }

    // MARK: Geometry

    func windowDidMove(_ notification: Notification) { remember() }
    func windowDidResize(_ notification: Notification) { remember() }

    func windowWillClose(_ notification: Notification) {
        flushGeometry()
        window?.delegate = nil
        webView?.navigationDelegate = nil
        webView?.uiDelegate = nil
        webView?.stopLoading()
        window = nil
        webView = nil
    }

    /// Records the window's geometry; it is saved once it has been still for 500 ms.
    private func remember() {
        guard let window, let g = Self.geometry(of: window) else { return }
        // Minimized windows report parked coordinates; never save those.
        guard !window.isMiniaturized, CanvasPlacement.plausible(g) else { return }
        pending = g
        settleTask?.cancel()
        settleTask = Task { [weak self] in
            try? await Task.sleep(for: CanvasPlacement.settleDelay)
            guard !Task.isCancelled else { return }
            self?.flushGeometry(waitForCore: false)
        }
    }

    /// Saves any pending geometry now (settled, window closing, app exiting). The settled save
    /// is queued on the core worker (`waitForCore: false`), in order with every other save;
    /// closing and quitting drain the worker, then save synchronously.
    func flushGeometry(waitForCore: Bool = true) {
        settleTask?.cancel()
        settleTask = nil
        guard let g = pending else { return }
        pending = nil
        lastSaved = g
        if waitForCore { saver.saveNow(g) } else { saver.saveSettled(g) }
    }

    /// Frame top-left (title bar) and content size, in the top-left space settings use.
    private static func geometry(of window: NSWindow) -> CanvasGeometry? {
        guard let primary = NSScreen.screens.first else { return nil }
        let tl = CanvasScreenSpace.topLeft(window.frame, primaryMaxY: primary.frame.maxY)
        let content = window.contentRect(forFrameRect: window.frame).size
        return CanvasGeometry(x: tl.x, y: tl.y, width: content.width, height: content.height)
    }

    // MARK: WKNavigationDelegate

    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction,
                 decisionHandler: @escaping @MainActor @Sendable (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url, navGuard.allows(url) else {
            Self.log.notice("blocked navigation to \(navigationAction.request.url?.absoluteString ?? "?", privacy: .public)")
            decisionHandler(.cancel)
            return
        }
        decisionHandler(.allow)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        Self.log.error("web content process ended; reloading")
        if let currentURL { webView.load(URLRequest(url: currentURL)) }
    }

    // MARK: WKUIDelegate

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        Self.log.notice("blocked new window for \(navigationAction.request.url?.absoluteString ?? "?", privacy: .public)")
        return nil
    }

    func webView(_ webView: WKWebView, requestMediaCapturePermissionFor origin: WKSecurityOrigin,
                 initiatedByFrame frame: WKFrameInfo, type: WKMediaCaptureType,
                 decisionHandler: @escaping @MainActor @Sendable (WKPermissionDecision) -> Void) {
        Self.log.notice("denied media capture permission request")
        decisionHandler(.deny)
    }

    func webView(_ webView: WKWebView, runOpenPanelWith parameters: WKOpenPanelParameters,
                 initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping @MainActor @Sendable ([URL]?) -> Void) {
        completionHandler(nil)
    }
}
