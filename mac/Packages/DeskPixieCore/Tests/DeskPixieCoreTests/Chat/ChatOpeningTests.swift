import DeskPixieCore
import XCTest

final class ChatOpeningTests: XCTestCase {
    func testAScrollAskedBeforeTheViewAppearsRunsOnAppear() {
        var o = ChatOpening()
        o.opening()
        XCTAssertFalse(o.scrollsOnAppear, "nothing asked yet")
        o.asked()
        XCTAssertTrue(o.scrollsOnAppear)
        o.scrolled()
        XCTAssertTrue(o.opened)
        XCTAssertFalse(o.scrollsOnAppear, "it ran")
    }

    /// Task 8 re-review: a new view made for the next open must not run the last open's scroll.
    func testTheLastOpensScrollIsNotPendingInTheNext() {
        var o = ChatOpening()
        o.opening()
        o.asked()
        o.scrolled()
        o.asked() // a pill, while open
        o.scrolled()
        o.opening() // closed, then shown again
        XCTAssertFalse(o.opened)
        XCTAssertFalse(o.scrollsOnAppear, "nothing is pending until this open asks")
        o.asked()
        XCTAssertTrue(o.scrollsOnAppear)
    }

    func testAScrollAskedButNotRunIsDroppedByTheNextOpen() {
        var o = ChatOpening()
        o.opening()
        o.asked()
        o.opening()
        XCTAssertFalse(o.scrollsOnAppear)
    }
}
