import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `src-tauri/src/canvas/nav.rs` tests (4 of 4).
final class CanvasNavGuardTests: XCTestCase {
    private func url(_ s: String) -> URL { URL(string: s)! }

    func testNothingIsAllowedBeforeTheFirstShow() {
        XCTAssertFalse(CanvasNavGuard().allows(url("canvas://localhost/pages/a.html")))
    }

    func testOnlyTheCurrentPageIsAllowed() {
        let g = CanvasNavGuard()
        g.allow(url("canvas://localhost/pages/a.html"))
        XCTAssertTrue(g.allows(url("canvas://localhost/pages/a.html")))
        for other in [
            "canvas://localhost/pages/b.html",
            "canvas://localhost/ds/CANVAS.md",
            "canvas://evil/pages/a.html",
            "https://example.com/",
            "http://localhost/pages/a.html",
            "file:///etc/passwd",
            "about:blank",
            "data:text/html,hi",
        ] {
            XCTAssertFalse(g.allows(url(other)), other)
        }
    }

    func testInPageAnchorsAreAllowedButQueriesAreNot() {
        let g = CanvasNavGuard()
        g.allow(url("canvas://localhost/pages/a.html"))
        XCTAssertTrue(g.allows(url("canvas://localhost/pages/a.html#details")))
        XCTAssertFalse(g.allows(url("canvas://localhost/pages/a.html?x=1")))
    }

    func testShowingAnotherPageRevokesTheOldOne() {
        let g = CanvasNavGuard()
        let alias = g
        g.allow(url("canvas://localhost/pages/a.html"))
        alias.allow(url("canvas://localhost/pages/b.html"))
        XCTAssertFalse(g.allows(url("canvas://localhost/pages/a.html")))
        XCTAssertTrue(g.allows(url("canvas://localhost/pages/b.html")))
    }

    // Swift-only: a different port, or a URL without a host, is another page.
    func testPortAndHostlessUrlsAreRefused() {
        let g = CanvasNavGuard()
        g.allow(url("canvas://localhost/pages/a.html"))
        XCTAssertFalse(g.allows(url("canvas://localhost:8080/pages/a.html")))
        XCTAssertFalse(g.allows(url("canvas:/pages/a.html")))
    }
}
