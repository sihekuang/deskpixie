import XCTest
@testable import DeskPixieCore

/// #240: what DeskPixie offers to attach, from what `detect` sees (no read).
final class ClipboardTests: XCTestCase {
    func testWhatCounts() {
        XCTAssertEqual(ClipboardRules.kind(["public.png"]), .image)
        XCTAssertEqual(ClipboardRules.kind(["public.tiff", "public.url"]), .image)
        XCTAssertEqual(ClipboardRules.kind(["public.jpeg"]), .image)
        XCTAssertEqual(ClipboardRules.kind(["public.file-url"]), .files)
        XCTAssertEqual(ClipboardRules.kind(["public.file-url", "public.utf8-plain-text"]), .files)
        XCTAssertEqual(ClipboardRules.kind(["public.utf8-plain-text"]), .none)
        XCTAssertEqual(ClipboardRules.kind(["public.rtf", "public.png"]), .none)
        XCTAssertEqual(ClipboardRules.kind(["public.html", "public.png", "public.utf8-plain-text"]), .none)
        XCTAssertEqual(ClipboardRules.kind(["public.png", "public.utf8-plain-text"]), .none)
        XCTAssertEqual(ClipboardRules.kind([]), .none)
    }

    func testHints() {
        XCTAssertEqual(ClipboardRules.hint(.image), "Screenshot copied")
        XCTAssertEqual(ClipboardRules.hint(.files), "File copied")
        XCTAssertNil(ClipboardRules.hint(.none))
    }

    func testCmdVPastesTextWhenThereIsText() {
        XCTAssertFalse(ClipboardRules.pasteAttaches(["public.utf8-plain-text"]))
        XCTAssertFalse(ClipboardRules.pasteAttaches(["public.html", "public.png", "public.utf8-plain-text"]))
        XCTAssertTrue(ClipboardRules.pasteAttaches(["public.png"]))
        XCTAssertTrue(ClipboardRules.pasteAttaches(["public.file-url", "public.utf8-plain-text"]))
        XCTAssertFalse(ClipboardRules.pasteAttaches([]))
    }
}
