import XCTest
@testable import DeskPixieCore

/// #286: Hide closes a deck until the pointer has left the region around Pixie.
final class CollapseLatchTests: XCTestCase {
    /// The reply deck: Hide puts the card away, hovering doesn't bring it back until the pointer
    /// has left, and a new card shows as usual.
    func testTheReplyDeckStaysAwayUntilThePointerHasLeft() {
        var latch = CollapseLatch()
        var chat = QuickChatModel()
        _ = chat.update(ReplyCard(id: 1, text: "one", count: 1, lines: [1]))
        _ = chat.update(ReplyCard(id: 2, text: "two", count: 1, lines: [2]))
        chat.dismiss()
        latch.collapse()
        XCTAssertFalse(chat.showsCard(hovering: latch.hover(true), always: false), "still around: stays away")
        latch.release() // the pointer left the region
        XCTAssertTrue(chat.showsCard(hovering: latch.hover(true), always: false), "back on Pixie: the earlier replies")
        latch.collapse()
        latch.release() // a new card arrives
        _ = chat.update(ReplyCard(id: 3, text: "three", count: 1, lines: [3]))
        XCTAssertTrue(chat.showsCard(hovering: latch.hover(true), always: false))
    }

    func testHoverCountsOnlyWhileOpen() {
        var latch = CollapseLatch()
        XCTAssertTrue(latch.hover(true))
        XCTAssertFalse(latch.hover(false))
        latch.collapse()
        XCTAssertTrue(latch.collapsed)
        XCTAssertFalse(latch.hover(true))
        latch.release()
        XCTAssertTrue(latch.hover(true))
    }
}
