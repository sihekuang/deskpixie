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

    // MARK: ChatShow

    func testARealOpenLoadsWaitsAndScrolls() {
        let s = ChatShow(opening: true, onScreen: false, entryID: nil, unread: 0)
        XCTAssertEqual(s, ChatShow(opening: true, onScreen: true, entryID: "e1", unread: 3))
        XCTAssertTrue(s.reloads && s.reopens && s.scrolls)
    }

    /// Review I1: unread came while the window was hidden; showing it opens it again (nothing
    /// is read until the scroll to the oldest unread ran) without reloading (M1).
    func testUnreadThatCameWhileHiddenWaitsForTheScroll() {
        let s = ChatShow(opening: false, onScreen: false, entryID: nil, unread: 2)
        XCTAssertTrue(s.reopens, "not marked read before the scroll")
        XCTAssertTrue(s.scrolls, "the open always runs its scroll")
        XCTAssertFalse(s.reloads, "older pages you scrolled into stay")
    }

    func testHiddenWithNothingNewStaysWhereItWas() {
        let s = ChatShow(opening: false, onScreen: false, entryID: nil, unread: 0)
        XCTAssertFalse(s.reloads || s.reopens || s.scrolls)
    }

    func testHiddenAtAnEntryScrollsThereWithoutReopening() {
        let s = ChatShow(opening: false, onScreen: false, entryID: "e4", unread: 0)
        XCTAssertTrue(s.scrolls)
        XCTAssertFalse(s.reopens || s.reloads)
    }

    func testAlreadyOnScreenOnlyScrolls() {
        let s = ChatShow(opening: false, onScreen: true, entryID: nil, unread: 2)
        XCTAssertTrue(s.scrolls)
        XCTAssertFalse(s.reopens, "what's in view may already count as read")
        XCTAssertFalse(s.reloads)
    }
}
