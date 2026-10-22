import AppKit
import XCTest
@testable import DeskPixieCore

/// #240: the waiting attachments as the shell sees them, and shrinking images to fit.
final class AttachmentModelTests: XCTestCase {
    private func d(_ s: String) -> EngineEvent? { EngineEvent.decode(Data(s.utf8)) }

    func testEvents() {
        let json = #"{"kind":"attachments","items":[{"id":"ab","name":"a.png","kind":"image","path":"/x/a.png","size":3}]}"#
        XCTAssertEqual(d(json), .attachments([AttachmentItem(id: "ab", name: "a.png", kind: "image", path: "/x/a.png", size: 3)]))
        XCTAssertEqual(d(#"{"kind":"attachments","items":[]}"#), .attachments([]))
        XCTAssertEqual(d(#"{"kind":"clipboard.read","request":"clip-1"}"#), .clipboardRead(request: "clip-1"))
        XCTAssertNil(d(#"{"kind":"clipboard.read"}"#))
    }

    func testApply() {
        var m = AttachmentModel()
        XCTAssertFalse(m.chipsVisible)
        let a = AttachmentItem(id: "1", name: "a.png", kind: "image", path: "/a", size: 1)
        m.notice = "That file is over 2 MB."
        m.apply([a])
        XCTAssertEqual(m.items, [a])
        XCTAssertTrue(m.chipsVisible)
        XCTAssertEqual(m.notice, "That file is over 2 MB.", "a refusal stays until the next try")
        m.apply([])
        XCTAssertFalse(m.chipsVisible)
    }

    private func png(_ side: Int, noise: Bool) -> Data {
        let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: side, pixelsHigh: side, bitsPerSample: 8,
                                   samplesPerPixel: 4, hasAlpha: true, isPlanar: false, colorSpaceName: .deviceRGB,
                                   bytesPerRow: 0, bitsPerPixel: 0)!
        var g = SystemRandomNumberGenerator()
        let p = rep.bitmapData!
        for i in 0..<(rep.bytesPerRow * side) { p[i] = noise ? UInt8.random(in: 0...255, using: &g) : 128 }
        return rep.representation(using: .png, properties: [:])!
    }

    func testABigImageShrinksToFit() {
        let big = png(1600, noise: true)
        XCTAssertGreaterThan(big.count, 2 << 20)
        guard let out = ImageShrink.fit(big, maxBytes: 2 << 20) else { return XCTFail("nil") }
        XCTAssertLessThanOrEqual(out.data.count, 2 << 20)
        XCTAssertNotNil(NSImage(data: out.data))
        XCTAssertTrue(["png", "jpg"].contains(out.ext))
    }

    func testASmallImageStaysPNG() {
        let small = png(10, noise: false)
        guard let out = ImageShrink.fit(small, maxBytes: 2 << 20) else { return XCTFail("nil") }
        XCTAssertEqual(out.ext, "png")
        XCTAssertEqual(out.data, small)
    }

    func testNotAnImage() {
        XCTAssertNil(ImageShrink.fit(Data("hello".utf8), maxBytes: 2 << 20))
    }
}
