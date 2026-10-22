import XCTest
@testable import DeskPixieCore

/// #240: Pixie offers each new copy of an image or files, once.
final class ClipboardWatchTests: XCTestCase {
    func testOffersEachNewCopyOnce() {
        var w = ClipboardWatch()
        XCTAssertFalse(w.poll(changeCount: 5, kind: .image), "what was copied before launch")
        XCTAssertFalse(w.poll(changeCount: 5, kind: .image))
        XCTAssertTrue(w.poll(changeCount: 6, kind: .image))
        XCTAssertFalse(w.poll(changeCount: 6, kind: .image))
        XCTAssertFalse(w.poll(changeCount: 7, kind: .none), "text")
        XCTAssertTrue(w.poll(changeCount: 8, kind: .files))
    }

    func testTheOfferFadesUnlessHovered() {
        var o = CopyOffer()
        let t0 = Date(timeIntervalSince1970: 100)
        o.show(.image, now: t0)
        XCTAssertEqual(o.kind, .image)
        XCTAssertFalse(o.expire(now: t0.addingTimeInterval(4.9), hovering: false))
        XCTAssertFalse(o.expire(now: t0.addingTimeInterval(6), hovering: true), "hover keeps it")
        XCTAssertTrue(o.expire(now: t0.addingTimeInterval(6.1), hovering: false))
        XCTAssertNil(o.kind)
        o.show(.files, now: t0)
        o.take()
        XCTAssertNil(o.kind)
    }
}
