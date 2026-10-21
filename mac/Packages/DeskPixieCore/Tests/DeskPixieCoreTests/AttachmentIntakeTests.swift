import AppKit
import XCTest
@testable import DeskPixieCore

/// #240: what the clipboard turns into, read only when the user asked.
final class AttachmentIntakeTests: XCTestCase {
    final class FakeClipboard: ClipboardSource {
        var changeCount = 1
        var types: Set<String> = []
        var png: Data?
        var files: [URL] = []
        var reads = 0
        func detectedTypes() -> Set<String> { types }
        func readImagePNG() -> Data? { reads += 1; return png }
        func readFileURLs() -> [URL] { reads += 1; return files }
    }

    private let at = Date(timeIntervalSince1970: 0)

    private func tinyPNG() -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 2, pixelsHigh: 2, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        return rep.representation(using: .png, properties: [:])!
    }

    func testAnImageBecomesAScreenshot() {
        let c = FakeClipboard()
        c.types = ["public.png"]
        c.png = tinyPNG()
        guard case .adds(let adds) = AttachmentIntake.fromClipboard(c, now: at) else { return XCTFail() }
        XCTAssertEqual(adds.count, 1)
        guard case .data(let name, let data) = adds[0] else { return XCTFail() }
        XCTAssertTrue(name.hasPrefix("Screenshot ") && name.hasSuffix(".png"), name)
        XCTAssertEqual(data, c.png)
    }

    func testFilesAreAddedByPath() {
        let c = FakeClipboard()
        c.types = ["public.file-url", "public.utf8-plain-text"]
        c.files = [URL(fileURLWithPath: "/tmp/a.pdf"), URL(fileURLWithPath: "/tmp/b.log")]
        guard case .adds(let adds) = AttachmentIntake.fromClipboard(c, now: at) else { return XCTFail() }
        XCTAssertEqual(adds, [.file("/tmp/a.pdf"), .file("/tmp/b.log")])
    }

    func testTextIsNeverReadOrTaken() {
        let c = FakeClipboard()
        c.types = ["public.utf8-plain-text"]
        XCTAssertEqual(AttachmentIntake.fromClipboard(c, now: at), .refused("There's no image or file on the clipboard."))
        XCTAssertEqual(c.reads, 0)
    }

    func testARefusedRead() {
        let c = FakeClipboard()
        c.types = ["public.png"] // macOS said no: the read came back empty
        XCTAssertEqual(AttachmentIntake.fromClipboard(c, now: at),
                       .refused("DeskPixie isn't allowed to read the clipboard. Allow it in System Settings, Privacy & Security, Paste from Other Apps."))
    }

    func testDroppedImageData() {
        guard case .adds(let adds) = AttachmentIntake.fromImage(tinyPNG(), now: at) else { return XCTFail() }
        guard case .data(let name, _) = adds[0] else { return XCTFail() }
        XCTAssertTrue(name.hasSuffix(".png"))
        XCTAssertEqual(AttachmentIntake.fromImage(Data("no".utf8), now: at), .refused("Couldn't read that image."))
    }
}
