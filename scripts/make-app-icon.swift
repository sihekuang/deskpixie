// Renders DeskPixie's 1024×1024 app icon master from Bit's sprite sheet.
// Run via scripts/make-app-icon.sh:  swift make-app-icon.swift <pack dir> <out.png>
//
// The icon is Bit's face filling the whole square, edge to edge: a 22×22 pixel-art square in
// Bit's palette (dark outline, light highlight along the top-left edge, shade along the
// bottom-right), with the face from idle frame 0 — eyes, blush and the closed mouth composited at
// the pack's mouth anchor, the same face MenuBarIcon uses — copied onto it pixel for pixel. Each
// grid pixel is a ~46 px block spanning the full 1024 px canvas.
import AppKit
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let canvas = 1024
/// Grid pixels per side.
let grid = 22
/// How far in from the edge (grid pixels, outline included) the highlight and shade reach.
let band = 3
/// Bit's face in the cell (eyes, blush, mouth): x 7…24, y 15…22, top-left origin.
let faceRect = (x: 7, y: 15, w: 18, h: 8)

/// Bit's body palette (pixies/bit/sheet.png).
func rgb(_ hex: UInt32) -> CGColor {
    CGColor(srgbRed: CGFloat((hex >> 16) & 0xff) / 255, green: CGFloat((hex >> 8) & 0xff) / 255,
            blue: CGFloat(hex & 0xff) / 255, alpha: 1)
}
let outlineHex: UInt32 = 0x064e3b, highlightHex: UInt32 = 0x6ee7b7
let baseHex: UInt32 = 0x10b981, shadeHex: UInt32 = 0x059669
let bodyHexes: Set<UInt32> = [outlineHex, highlightHex, baseHex, shadeHex]

func fail(_ msg: String) -> Never {
    FileHandle.standardError.write(Data("error: \(msg)\n".utf8))
    exit(1)
}

let args = CommandLine.arguments
guard args.count == 3 else { fail("usage: make-app-icon.swift <pack dir> <out.png>") }
let packDir = URL(fileURLWithPath: args[1])
let outURL = URL(fileURLWithPath: args[2])

struct Pack: Decodable {
    struct State: Decodable { var row: Int }
    struct Mouth: Decodable { var row: Int; var anchor: [Int]; var size: [Int]; var levels: Int }
    var cell: [Int]
    var sheet: String?
    var states: [String: State]
    var mouth: Mouth?
}

guard let json = try? Data(contentsOf: packDir.appendingPathComponent("pixie.json")),
      let pack = try? JSONDecoder().decode(Pack.self, from: json) else { fail("cannot read \(packDir.path)/pixie.json") }
let sheetURL = packDir.appendingPathComponent(pack.sheet ?? "sheet.png")
guard let src = CGImageSourceCreateWithURL(sheetURL as CFURL, nil),
      let sheet = CGImageSourceCreateImageAtIndex(src, 0, nil) else { fail("cannot read \(sheetURL.path)") }
let w = pack.cell[0], h = pack.cell[1]
guard let idle = pack.states["idle"],
      let body = sheet.cropping(to: CGRect(x: 0, y: idle.row * h, width: w, height: h)) else { fail("no idle frame") }

// 1. The cell: body + closed mouth (level 0), in a top-left-origin RGBA buffer.
let space = CGColorSpace(name: CGColorSpace.sRGB)!
let alphaInfo = CGImageAlphaInfo.premultipliedLast.rawValue
guard let cell = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0,
                           space: space, bitmapInfo: alphaInfo) else { fail("context") }
cell.interpolationQuality = .none
cell.draw(body, in: CGRect(x: 0, y: 0, width: w, height: h))
if let m = pack.mouth, m.levels > 0,
   let mouth = sheet.cropping(to: CGRect(x: 0, y: m.row * h, width: m.size[0], height: m.size[1])) {
    // CG's origin is bottom-left; the anchor is top-left in cell pixels.
    cell.draw(mouth, in: CGRect(x: m.anchor[0], y: h - m.anchor[1] - m.size[1], width: m.size[0], height: m.size[1]))
}
guard faceRect.x + faceRect.w <= w, faceRect.y + faceRect.h <= h else { fail("face outside the cell") }

// 2. The square: an outline, then the highlight and shade inside it.
// tile[y][x] is a 0xRRGGBB colour; y 0 is the top row.
var tile = [[UInt32]](repeating: [UInt32](repeating: baseHex, count: grid), count: grid)
let n = Double(grid)
for y in 0..<grid { for x in 0..<grid {
    // Grid pixels from here to the nearest edge, counting this one.
    let depth = min(x, y, grid - 1 - x, grid - 1 - y) + 1
    if depth == 1 { tile[y][x] = outlineHex; continue }
    guard depth <= band else { continue }
    if Double(x + y) < n * 0.75 { tile[y][x] = highlightHex }
    else if Double(x + y) > n * 1.25 { tile[y][x] = shadeHex }
} }

// 3. The face, copied from the cell into the middle of the tile.
let data = cell.data!.assumingMemoryBound(to: UInt8.self)
let stride = cell.bytesPerRow
let fx = (grid - faceRect.w) / 2, fy = (grid - faceRect.h) / 2
for y in 0..<faceRect.h { for x in 0..<faceRect.w {
    let i = (faceRect.y + y) * stride + (faceRect.x + x) * 4
    guard data[i + 3] == 255 else { continue }
    let hex = UInt32(data[i]) << 16 | UInt32(data[i + 1]) << 8 | UInt32(data[i + 2])
    if !bodyHexes.contains(hex) { tile[fy + y][fx + x] = hex }
} }

// 4. The canvas: each grid pixel as a block, edges rounded to whole pixels so they tile exactly.
guard let ctx = CGContext(data: nil, width: canvas, height: canvas, bitsPerComponent: 8, bytesPerRow: 0,
                          space: space, bitmapInfo: alphaInfo) else { fail("context") }
func edge(_ i: Int) -> Int { i * canvas / grid }
func blockRect(_ x: Int, _ y: Int) -> CGRect {
    // CG's origin is bottom-left; grid row 0 is the top.
    CGRect(x: edge(x), y: canvas - edge(y + 1), width: edge(x + 1) - edge(x), height: edge(y + 1) - edge(y))
}
ctx.setShouldAntialias(false)

for y in 0..<grid { for x in 0..<grid {
    ctx.setFillColor(rgb(tile[y][x]))
    ctx.fill(blockRect(x, y))
} }

guard let out = ctx.makeImage(),
      let dest = CGImageDestinationCreateWithURL(outURL as CFURL, UTType.png.identifier as CFString, 1, nil) else { fail("output") }
CGImageDestinationAddImage(dest, out, nil)
guard CGImageDestinationFinalize(dest) else { fail("cannot write \(outURL.path)") }
print("master: \(outURL.path) (Bit's face on a \(grid)×\(grid) grid)")
