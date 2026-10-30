import AppKit
import XCTest
@testable import DeskPixieCore

/// The menu bar and Dock icons follow the chosen pixie (#412): every built-in pixie, and any
/// custom one a pack can describe (any cell size, any colours).
@MainActor
final class PixieIconsTests: XCTestCase {
    /// RGB colours for the tests' made-up pixies; nil is a clear pixel.
    typealias Color = (r: UInt8, g: UInt8, b: UInt8)?

    /// A two-row sheet: the body (`body(x, y)`) in row 0 as idle, and a closed mouth in row 1.
    /// `shadow(x, y)` marks clear pixels drawn as a faint black (alpha 20), like a soft shadow.
    private func custom(cell: (w: Int, h: Int), mouth: [[Color]] = [[nil]], anchor: (x: Int, y: Int) = (0, 0),
                        idleFaceDy: [Int] = [], shadow: (Int, Int) -> Bool = { _, _ in false },
                        body: (Int, Int) -> Color) throws -> (Pack, SpriteSheet) {
        let w = cell.w, h = cell.h
        var rgba = [UInt8](repeating: 0, count: w * h * 2 * 4)
        func put(_ x: Int, _ y: Int, _ c: Color) {
            guard let c else { return }
            let i = (y * w + x) * 4
            rgba[i] = c.r; rgba[i + 1] = c.g; rgba[i + 2] = c.b; rgba[i + 3] = 255
        }
        for y in 0..<h { for x in 0..<w {
            put(x, y, body(x, y))
            if body(x, y) == nil, shadow(x, y) { rgba[(y * w + x) * 4 + 3] = 20 }
        } }
        for (y, row) in mouth.enumerated() { for (x, c) in row.enumerated() { put(x, h + y, c) } }
        let provider = try XCTUnwrap(CGDataProvider(data: Data(rgba) as CFData))
        let image = try XCTUnwrap(CGImage(width: w, height: h * 2, bitsPerComponent: 8, bitsPerPixel: 32,
                                          bytesPerRow: w * 4, space: CGColorSpaceCreateDeviceRGB(),
                                          bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
                                          provider: provider, decode: nil, shouldInterpolate: false,
                                          intent: .defaultIntent))
        let idle = Pack.StateSpec(row: 0, frames: 1, loop: true, bob: true, faceDy: idleFaceDy)
        let other = Pack.StateSpec(row: 0, frames: 1, loop: true, bob: true, faceDy: [])
        let pack = Pack(name: "Custom", cell: [w, h], fps: 8, bob: Pack.Bob(px: 0, frames: 1),
                        states: ["idle": idle, "listening": other, "thinking": other],
                        mouth: Pack.Mouth(row: 1, anchor: [anchor.x, anchor.y],
                                          size: [mouth.first?.count ?? 1, mouth.count], levels: 2))
        return (pack, SpriteSheet(image: image))
    }

    private func bundled(_ id: String) throws -> (Pack, SpriteSheet) {
        // Tests/DeskPixieCoreTests/Parity → the repo's pixies/<id>.
        var dir = URL(fileURLWithPath: #filePath)
        for _ in 0..<7 { dir.deleteLastPathComponent() }
        return try XCTUnwrap(PackFolder.load(dir.appendingPathComponent("pixies/\(id)")), id)
    }

    private func pixels(_ img: CGImage) -> [UInt8] {
        let w = img.width, h = img.height
        var rgba = [UInt8](repeating: 0, count: w * h * 4)
        rgba.withUnsafeMutableBytes { buf in
            let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                space: CGColorSpaceCreateDeviceRGB(),
                                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
            ctx.draw(img, in: CGRect(x: 0, y: 0, width: w, height: h))
        }
        return rgba
    }

    private func onCount(_ img: CGImage) -> Int {
        let a = pixels(img)
        return stride(from: 3, to: a.count, by: 4).filter { a[$0] > 0 }.count
    }

    // MARK: Menu bar

    func testEveryBuiltInPixieKeepsMostOfItsShapeInTheMenuBar() throws {
        for id in ["bit", "dino", "blackgold", "flash"] {
            let (pack, sheet) = try bundled(id)
            let face = try XCTUnwrap(PixieFace.render(pack: pack, sheet: sheet), id)
            let mask = try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet), id)
            let b = try XCTUnwrap(face.opaqueBounds)
            XCTAssertEqual(mask.width, b.width, id)
            XCTAssertEqual(mask.height, b.height, id)
            let opaque = face.rgba.enumerated().filter { $0.offset % 4 == 3 && $0.element > 0 }.count
            XCTAssertGreaterThanOrEqual(Double(onCount(mask)), MenuBarIcon.minKeptShare * Double(opaque), id)
            XCTAssertNotNil(MenuBarIcon.image(pack: pack, sheet: sheet), id)
        }
    }

    func testADarkPixieWithBrightEyesGetsTheEyesCutOut() throws {
        let dark: Color = (20, 20, 30), eye: Color = (250, 200, 40)
        let (pack, sheet) = try custom(cell: (12, 12)) { x, y in
            (x == 4 || x == 7) && y == 5 ? eye : dark
        }
        let a = pixels(try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet)))
        XCTAssertEqual(a[(5 * 12 + 4) * 4 + 3], 0, "eye cut out")
        XCTAssertEqual(a[(5 * 12 + 7) * 4 + 3], 0, "eye cut out")
        XCTAssertEqual(a[(6 * 12 + 4) * 4 + 3], 255, "body stays")
    }

    func testTheOutlineAndThinPartsStayEvenWhenDark() throws {
        // A light body with a dark one-pixel outline and a dark antenna on top.
        let light: Color = (240, 220, 120), dark: Color = (40, 30, 20)
        let (pack, sheet) = try custom(cell: (10, 12)) { x, y in
            if y < 3 { return x == 5 ? dark : nil }
            return x == 0 || x == 9 || y == 3 || y == 11 ? dark : light
        }
        let mask = try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet))
        XCTAssertEqual(mask.width, 10)
        XCTAssertEqual(mask.height, 12, "the antenna is kept")
        let a = pixels(mask)
        XCTAssertEqual(a[(0 * 10 + 5) * 4 + 3], 255, "antenna tip")
        XCTAssertEqual(a[(7 * 10 + 0) * 4 + 3], 255, "outline")
    }

    func testAPixieThatIsMostlyFeaturesFallsBackToItsSilhouette() throws {
        // Stripes of white, grey and black: two thirds of the inner pixels stand out from the grey
        // median, so cutting would leave specks.
        let (pack, sheet) = try custom(cell: (16, 16)) { x, y in
            [(255, 255, 255), (128, 128, 128), (0, 0, 0)][(x + y) % 3]
        }
        let mask = try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet))
        XCTAssertEqual(onCount(mask), 256)
    }

    func testAnEmptyIdleFrameHasNoIcon() throws {
        let (pack, sheet) = try custom(cell: (8, 8)) { _, _ in nil }
        XCTAssertNil(MenuBarIcon.faceMask(pack: pack, sheet: sheet))
        XCTAssertNil(DockIcon.cgImage(pack: pack, sheet: sheet))
    }

    func testTheClosedMouthIsDrawnAtTheAnchor() throws {
        let body: Color = (100, 180, 100), lip: Color = (200, 40, 40)
        let (pack, sheet) = try custom(cell: (8, 8), mouth: [[lip, lip]], anchor: (3, 6)) { _, _ in body }
        let face = try XCTUnwrap(PixieFace.render(pack: pack, sheet: sheet))
        let i = (6 * 8 + 3) * 4
        XCTAssertEqual(Array(face.rgba[i..<i + 4]), [200, 40, 40, 255])
    }

    func testTheMouthMovesWithTheIdleFramesFaceDy() throws {
        let body: Color = (100, 180, 100), lip: Color = (200, 40, 40)
        let (pack, sheet) = try custom(cell: (8, 8), mouth: [[lip, lip]], anchor: (3, 6), idleFaceDy: [-2]) { _, _ in body }
        let face = try XCTUnwrap(PixieFace.render(pack: pack, sheet: sheet))
        let i = (4 * 8 + 3) * 4
        XCTAssertEqual(Array(face.rgba[i..<i + 4]), [200, 40, 40, 255], "two pixels up, as on screen")
    }

    func testAFaintShadowIsNotPartOfTheMenuBarShape() throws {
        // A light 6×6 body with a dark outline, inside a one-pixel faint shadow.
        let light: Color = (240, 220, 120), dark: Color = (40, 30, 20)
        let (pack, sheet) = try custom(cell: (8, 8), shadow: { _, _ in true }) { x, y in
            guard (1...6).contains(x), (1...6).contains(y) else { return nil }
            return x == 1 || x == 6 || y == 1 || y == 6 ? dark : light
        }
        let mask = try XCTUnwrap(MenuBarIcon.faceMask(pack: pack, sheet: sheet))
        XCTAssertEqual(mask.width, 6)
        XCTAssertEqual(mask.height, 6)
        XCTAssertEqual(onCount(mask), 36, "the outline is still the edge, so none of it is cut")
    }

    // MARK: Dock

    func testBitStandsAsTallAsTheIconGridOnAClearCanvas() throws {
        let (pack, sheet) = try bundled("bit")
        let img = try XCTUnwrap(DockIcon.cgImage(pack: pack, sheet: sheet))
        XCTAssertEqual(img.width, 1024)
        XCTAssertEqual(img.height, 1024)
        // Bit is 23×28 sprite pixels: ×29 is the largest whole factor within 824.
        XCTAssertEqual(DockIcon.artRect(width: 23, height: 28), CGRect(x: 178, y: 106, width: 667, height: 812))
        let a = pixels(img)
        XCTAssertEqual(a[3], 0, "corner is clear")
        XCTAssertEqual(a[(512 * 1024 + 512) * 4 + 3], 255, "middle is Bit")
    }

    func testEveryBuiltInPixieHasADockIcon() throws {
        for id in ["bit", "dino", "blackgold", "flash"] {
            let (pack, sheet) = try bundled(id)
            let img = try XCTUnwrap(DockIcon.image(pack: pack, sheet: sheet), id)
            XCTAssertEqual(img.size, NSSize(width: 1024, height: 1024), id)
        }
    }

    func testPixelArtScalesByWholeFactorsAndBigArtScalesDownToTheGrid() {
        XCTAssertEqual(DockIcon.artRect(width: 32, height: 32), CGRect(x: 112, y: 112, width: 800, height: 800))
        // A custom pixie with a non-square cell.
        XCTAssertEqual(DockIcon.artRect(width: 48, height: 64), CGRect(x: 224, y: 128, width: 576, height: 768))
        // Bigger than the grid: fit it, not overflow.
        let big = DockIcon.artRect(width: 1200, height: 600)!
        XCTAssertEqual(big.width, 824, accuracy: 0.001)
        XCTAssertEqual(big.height, 412, accuracy: 0.001)
        XCTAssertNil(DockIcon.artRect(width: 0, height: 10))
    }
}
