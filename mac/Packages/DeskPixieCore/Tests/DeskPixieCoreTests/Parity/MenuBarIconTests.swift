import AppKit
import XCTest
@testable import DeskPixieCore

/// The menu bar item is Bit's face (idle frame 0 plus the closed mouth) as an 18 pt template
/// image, so macOS tints it for a light or dark menu bar. `sparkles` only when loading fails.
@MainActor
final class MenuBarIconTests: XCTestCase {
    private func fixture(_ name: String, _ ext: String) throws -> URL {
        try XCTUnwrap(Bundle.module.url(forResource: name, withExtension: ext, subdirectory: "Fixtures"))
    }

    private func bit() throws -> (Pack, SpriteSheet) {
        let pack = try XCTUnwrap(PackPayload.decode(Data(contentsOf: fixture("bit-pack", "json")))).pack
        return (pack, try XCTUnwrap(SpriteSheet(url: fixture("bit-sheet", "png"))))
    }

    private func alpha(_ img: CGImage) -> [[UInt8]] {
        let w = img.width, h = img.height
        var rgba = [UInt8](repeating: 0, count: w * h * 4)
        rgba.withUnsafeMutableBytes { buf in
            let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                space: CGColorSpaceCreateDeviceRGB(),
                                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
            ctx.draw(img, in: CGRect(x: 0, y: 0, width: w, height: h))
        }
        return (0..<h).map { y in (0..<w).map { x in rgba[(y * w + x) * 4 + 3] } }
    }

    func testFaceMaskIsBitsIdleFrameWithTheEyesPunchedOut() throws {
        let (pack, sheet) = try bit()
        let mask = try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet))
        let a = alpha(mask)
        XCTAssertLessThanOrEqual(mask.width, pack.cellWidth)
        XCTAssertLessThanOrEqual(mask.height, pack.cellHeight)
        XCTAssertTrue(a.allSatisfy { $0.allSatisfy { $0 == 0 || $0 == 255 } }, "pure on/off pixels (template)")
        // Trimmed to Bit: every edge row and column has a pixel.
        XCTAssertTrue(a.first!.contains(255))
        XCTAssertTrue(a.last!.contains(255))
        XCTAssertTrue(a.contains { $0.first == 255 })
        XCTAssertTrue(a.contains { $0.last == 255 })
        // The face reads: some row has a hole between opaque pixels (eyes or mouth).
        let holed = a.contains { row in
            guard let l = row.firstIndex(of: 255), let r = row.lastIndex(of: 255) else { return false }
            return row[l...r].contains(0)
        }
        XCTAssertTrue(holed, "dark features are cut out of the silhouette")
    }

    func testImageIsAnEighteenPointTemplate() throws {
        let (pack, sheet) = try bit()
        let img = try XCTUnwrap(MenuBarIcon.image(pack: pack, sheet: sheet))
        XCTAssertTrue(img.isTemplate)
        XCTAssertEqual(img.size, NSSize(width: 18, height: 18))
        XCTAssertEqual(img.accessibilityDescription, "DeskPixie")
    }

    func testLoadsFromAPackFolderAndFallsBackToSparkles() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("icon-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let (pack, _) = try bit()
        _ = pack
        // A pack folder as bundled: pixie.json (manifest) + sheet.png.
        let manifest = try JSONSerialization.jsonObject(with: Data(contentsOf: fixture("bit-pack", "json"))) as! [String: Any]
        try JSONSerialization.data(withJSONObject: manifest["pack"]!).write(to: dir.appendingPathComponent("pixie.json"))
        try FileManager.default.copyItem(at: fixture("bit-sheet", "png"), to: dir.appendingPathComponent("sheet.png"))
        XCTAssertNotNil(MenuBarIcon.load(packDirectory: dir))

        let missing = dir.appendingPathComponent("nope")
        XCTAssertNil(MenuBarIcon.load(packDirectory: missing))
        let fallback = MenuBarIcon.imageOrFallback(packDirectory: missing)
        XCTAssertTrue(fallback.isTemplate)
        XCTAssertEqual(MenuBarIcon.fallbackSymbol, "sparkles")
        XCTAssertEqual(MenuBarIcon.tooltip, "DeskPixie")
    }

    /// A pack made before the pixie rename (#68) has only `mascot.json`: it still loads.
    func testLoadsAPackWithOnlyAPreRenameManifest() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("icon-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let manifest = try JSONSerialization.jsonObject(with: Data(contentsOf: fixture("bit-pack", "json"))) as! [String: Any]
        try JSONSerialization.data(withJSONObject: manifest["pack"]!).write(to: dir.appendingPathComponent("mascot.json"))
        try FileManager.default.copyItem(at: fixture("bit-sheet", "png"), to: dir.appendingPathComponent("sheet.png"))
        XCTAssertNotNil(MenuBarIcon.load(packDirectory: dir))
        XCTAssertEqual(MenuBarIcon.manifestNames, ["pixie.json", "mascot.json"])
    }
}
