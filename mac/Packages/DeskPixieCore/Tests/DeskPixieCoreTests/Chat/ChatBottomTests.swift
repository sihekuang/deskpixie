import DeskPixieCore
import XCTest

@MainActor
final class ChatBottomTests: XCTestCase {
    /// Found in CI (#316, after the first fix): the window opens on an empty thread, the opening
    /// scroll runs, then the page comes in. The empty layout was never reported as a change, so
    /// a kept-only at-bottom stayed false, the page didn't follow, and the newest line never
    /// showed. At-bottom comes from the layout itself.
    func testTheOpeningPageFollowsFromAnEmptyThread() {
        var b = ChatBottom()
        // The empty thread: 40 pt of padding in a 500 pt viewport, never announced.
        XCTAssertTrue(b.atBottom(sentinelMaxY: 40, viewportHeight: 500), "at the bottom from the first layout")
        // The opening scroll ran (content fits: nothing to wait for), then 40 lines came in.
        XCTAssertTrue(b.laidOut(oldMaxY: 40, oldHeight: 40, newMaxY: 2040, newHeight: 2040,
                                viewportHeight: 500, following: true), "scrolls to the newest")
        XCTAssertTrue(b.atBottom(sentinelMaxY: 2040, viewportHeight: 500), "and stays at the bottom meanwhile")
        // The scroll lands: same height, end at the edge.
        XCTAssertFalse(b.laidOut(oldMaxY: 2040, oldHeight: 2040, newMaxY: 500, newHeight: 2040,
                                 viewportHeight: 500, following: true))
        XCTAssertTrue(b.atBottom(sentinelMaxY: 500, viewportHeight: 500))
    }

    func testBeforeTheOpeningScrollOnlyTheEndCounts() {
        var b = ChatBottom()
        XCTAssertFalse(b.laidOut(oldMaxY: 40, oldHeight: 40, newMaxY: 2040, newHeight: 2040,
                                 viewportHeight: 500, following: false))
        XCTAssertFalse(b.atBottom(sentinelMaxY: 2040, viewportHeight: 500), "the page came in at the top")
        XCTAssertFalse(b.resized(sentinelMaxY: 500, oldViewport: 500, newViewport: 460, following: false))
        XCTAssertFalse(b.held)
    }

    /// Found in CI on macOS 15 (#316): after a chip shrank the viewport from 544 to 514, the
    /// lazy stack's height flickered between 2143 and 2264 and the end between 403 and 544, so
    /// at-bottom flipped with each estimate and "↓ Latest" stayed up.
    func testTheBoxGrowingAndTheHeightFlickeringKeepTheBottom() {
        var b = ChatBottom()
        XCTAssertTrue(b.resized(sentinelMaxY: 423, oldViewport: 544, newViewport: 514, following: true))
        var old = (423.0, 2143.0)
        for new in [(544.0, 2264.0), (403, 2143), (524, 2264)] {
            _ = b.laidOut(oldMaxY: old.0, oldHeight: old.1, newMaxY: new.0, newHeight: new.1,
                          viewportHeight: 514, following: true)
            XCTAssertTrue(b.atBottom(sentinelMaxY: new.0, viewportHeight: 514), "end at \(new.0), \(new.1) tall")
            old = new
        }
        XCTAssertTrue(b.laidOut(oldMaxY: 403, oldHeight: 2143, newMaxY: 524, newHeight: 2264,
                                viewportHeight: 514, following: true), "the end out of view: scroll back")
    }

    func testAScrollLetsGo() {
        var b = ChatBottom()
        _ = b.resized(sentinelMaxY: 500, oldViewport: 500, newViewport: 460, following: true)
        XCTAssertTrue(b.held)
        XCTAssertFalse(b.laidOut(oldMaxY: 500, oldHeight: 2000, newMaxY: 900, newHeight: 2000,
                                 viewportHeight: 460, following: true))
        XCTAssertFalse(b.atBottom(sentinelMaxY: 900, viewportHeight: 460), "you scrolled up")
        XCTAssertFalse(b.laidOut(oldMaxY: 900, oldHeight: 2000, newMaxY: 1000, newHeight: 2100,
                                 viewportHeight: 460, following: true), "reading further up while rows measure")
        XCTAssertFalse(b.atBottom(sentinelMaxY: 1000, viewportHeight: 460))
        XCTAssertFalse(b.resized(sentinelMaxY: 1000, oldViewport: 460, newViewport: 500, following: true),
                       "scrolled up: left where you were")
    }

    func testTheOpeningScrollsLayoutSettlesIt() {
        var b = ChatBottom()
        _ = b.resized(sentinelMaxY: 500, oldViewport: 500, newViewport: 460, following: true)
        b.settled()
        XCTAssertFalse(b.atBottom(sentinelMaxY: 900, viewportHeight: 460), "landed on the oldest unread")
        XCTAssertFalse(b.atBottom(sentinelMaxY: .infinity, viewportHeight: 460), "not laid out")
    }

    /// macOS 15, opened from Pixie's button with DeskPixie in the background: the opening scroll
    /// ran but the window came up at the top, under "↓ Latest". Until it is seen at the bottom,
    /// each layout scrolls there again.
    func testAnOpeningScrollThatDidntLandScrollsAgain() {
        var b = ChatBottom()
        b.aimed(atBottom: true)
        XCTAssertTrue(b.landing)
        XCTAssertFalse(b.landed(sentinelMaxY: 2040, viewportHeight: 0), "no viewport yet: wait for it")
        XCTAssertTrue(b.landed(sentinelMaxY: 2040, viewportHeight: 500), "at the top: again")
        XCTAssertTrue(b.landed(sentinelMaxY: 2040, viewportHeight: 500))
        XCTAssertFalse(b.landed(sentinelMaxY: 500, viewportHeight: 500), "there")
        XCTAssertFalse(b.landing)
        XCTAssertFalse(b.landed(sentinelMaxY: 2040, viewportHeight: 500), "a scroll up after that is yours")
    }

    func testItTriesAFewTimesOnly() {
        var b = ChatBottom()
        b.aimed(atBottom: true)
        let tries = (0..<10).filter { _ in b.landed(sentinelMaxY: 2040, viewportHeight: 500) }.count
        XCTAssertEqual(tries, ChatBottom.landingTries)
        XCTAssertFalse(b.landing)
    }

    func testAnOpeningAtAMessageIsLeftWhereItIs() {
        var b = ChatBottom()
        b.aimed(atBottom: false)
        XCTAssertFalse(b.landing)
        XCTAssertFalse(b.landed(sentinelMaxY: 2040, viewportHeight: 500))
    }
}
