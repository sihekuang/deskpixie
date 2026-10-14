import CoreGraphics
import XCTest
@testable import DeskPixieCore

final class SpriteSheetTests: XCTestCase {
    /// 4×2 RGBA image, transparent except one opaque red pixel at (1, 0) (top-left origin).
    private func makeImage() -> CGImage {
        let w = 4, h = 2
        var bytes = [UInt8](repeating: 0, count: w * h * 4)
        let i = (0 * w + 1) * 4
        bytes[i] = 255; bytes[i + 3] = 255
        let provider = CGDataProvider(data: Data(bytes) as CFData)!
        return CGImage(width: w, height: h, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: w * 4,
                       space: CGColorSpaceCreateDeviceRGB(),
                       bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
                       provider: provider, decode: nil, shouldInterpolate: false, intent: .defaultIntent)!
    }

    func testIsOpaque() {
        let sheet = SpriteSheet(image: makeImage())
        XCTAssertEqual(sheet.width, 4)
        XCTAssertEqual(sheet.height, 2)
        XCTAssertTrue(sheet.isOpaque(sheetPoint: CGPoint(x: 1, y: 0)))
        XCTAssertTrue(sheet.isOpaque(sheetPoint: CGPoint(x: 1.7, y: 0.9)), "fractions floor into the pixel")
        XCTAssertFalse(sheet.isOpaque(sheetPoint: CGPoint(x: 0, y: 0)))
        XCTAssertFalse(sheet.isOpaque(sheetPoint: CGPoint(x: 1, y: 1)))
        XCTAssertFalse(sheet.isOpaque(sheetPoint: CGPoint(x: 4, y: 0)), "out of bounds")
        XCTAssertFalse(sheet.isOpaque(sheetPoint: CGPoint(x: -0.5, y: 0)), "out of bounds")
        XCTAssertFalse(sheet.isOpaque(sheetPoint: CGPoint(x: 1, y: 2)), "out of bounds")
    }

    func testCropReturnsTheRightSizeAndIsCached() throws {
        let sheet = SpriteSheet(image: makeImage())
        let c = try XCTUnwrap(sheet.crop(CGRect(x: 1, y: 0, width: 2, height: 2)))
        XCTAssertEqual(c.width, 2)
        XCTAssertEqual(c.height, 2)
        XCTAssertTrue(sheet.crop(CGRect(x: 1, y: 0, width: 2, height: 2)) === c)
    }

    func testCropOutsideTheSheetIsNil() {
        XCTAssertNil(SpriteSheet(image: makeImage()).crop(CGRect(x: 8, y: 8, width: 2, height: 2)))
    }

    func testLoadsARealSheetFromAURL() throws {
        // The bundled Bit sheet: 8 columns × 6 rows of 32 px cells.
        let url = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().appendingPathComponent("../../../../../mascots/bit/sheet.png")
            .standardizedFileURL
        try XCTSkipUnless(FileManager.default.fileExists(atPath: url.path), "mascots/ not present")
        let sheet = try XCTUnwrap(SpriteSheet(url: url))
        XCTAssertEqual(sheet.width, 256)
        XCTAssertEqual(sheet.height, 192)
    }
}
