import Foundation

/// The canvas navigation guard (port of `src-tauri/src/canvas/nav.rs`): the window may only be
/// on the page the app put there. Links, redirects, `location = …` and form posts elsewhere are
/// refused. A reference type, so every holder sees the latest `allow`.
public final class CanvasNavGuard: @unchecked Sendable {
    private let lock = NSLock()
    private var allowed: URLComponents?

    public init() {}

    /// Called right before the window is navigated to `url`.
    public func allow(_ url: URL) {
        let c = URLComponents(url: url, resolvingAgainstBaseURL: false)
        lock.withLock { allowed = c }
    }

    /// Same scheme, host, port, path and query as the allowed URL; the `#fragment` may differ
    /// (in-page anchors).
    public func allows(_ url: URL) -> Bool {
        guard let a = lock.withLock({ allowed }),
              let u = URLComponents(url: url, resolvingAgainstBaseURL: false) else { return false }
        return u.scheme?.lowercased() == a.scheme?.lowercased()
            && u.percentEncodedHost == a.percentEncodedHost
            && u.port == a.port
            && u.percentEncodedPath == a.percentEncodedPath
            && u.percentEncodedQuery == a.percentEncodedQuery
    }
}
