import CoreGraphics
@testable import DeskPixieCore
import XCTest

final class SentAttachmentTests: XCTestCase {
    func testPicturesAreTheCoresImageKinds() {
        for path in ["/s/Screenshot 12.30.56.png", "/s/a.JPG", "/s/b.jpeg", "/s/c.gif", "/s/d.webp"] {
            XCTAssertTrue(SentAttachment.isImage(path), path)
        }
        for path in ["/s/notes.pdf", "/s/png", "/s/a.png.txt", "/s/", ""] {
            XCTAssertFalse(SentAttachment.isImage(path), path)
        }
    }

    func testAThumbnailFitsAndKeepsItsShape() {
        // A wide screenshot is as wide as allowed; a tall one as tall.
        XCTAssertEqual(SentAttachment.thumbnailSize(CGSize(width: 1296, height: 816)), CGSize(width: 220, height: 139))
        XCTAssertEqual(SentAttachment.thumbnailSize(CGSize(width: 400, height: 1000)), CGSize(width: 56, height: 140))
        // A small one isn't blown up.
        XCTAssertEqual(SentAttachment.thumbnailSize(CGSize(width: 100, height: 60)), CGSize(width: 100, height: 60))
    }

    func testAPictureWithNoSizeHasNoThumbnail() {
        XCTAssertNil(SentAttachment.thumbnailSize(.zero))
        XCTAssertNil(SentAttachment.thumbnailSize(CGSize(width: 10, height: 0)))
    }
}
