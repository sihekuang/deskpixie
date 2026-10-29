// Renders DeskPixie's 1024×1024 app icon master from Bit's sprite sheet.
// Run via scripts/make-app-icon.sh:  swift make-app-icon.swift <pack dir> <out.png>
//
// The icon is Bit's face filling the whole tile: a 22×22 pixel-art rounded square in Bit's
// palette (dark outline, light highlight along the top-left edge, shade along the bottom-right),
// with the face from idle frame 0 — eyes, blush and the closed mouth composited at the pack's
// mouth anchor, the same face MenuBarIcon uses — copied onto it pixel for pixel. Each grid
// pixel is a 37 px block, centred on the macOS 824 px icon grid, with a soft drop shadow.
import AppKit
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let canvas = 1024
/// Grid pixels per side, and the corner radius in grid pixels (~22% of the side, like macOS).
let grid = 22
let cornerRadius = 5.0
/// How far in from the edge (grid pixels, outline included) the highlight and shade reach.
let band = 3
/// Screen pixels per grid pixel: the largest whole size that fits the 824 px icon grid.
let block = 824 / grid
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

// 2. The tile: a pixel-art rounded square, then its highlight, shade and outline.
// grid[y][x] is a 0xRRGGBB colour, or nil outside the tile; y 0 is the top row.
var tile = [[UInt32?]](repeating: [UInt32?](repeating: nil, count: grid), count: grid)
let n = Double(grid)
for y in 0..<grid { for x in 0..<grid {
    let cx = Double(x) + 0.5, cy = Double(y) + 0.5
    let qx = max(cornerRadius - cx, 0, cx - (n - cornerRadius))
    let qy = max(cornerRadius - cy, 0, cy - (n - cornerRadius))
    if qx * qx + qy * qy <= cornerRadius * cornerRadius { tile[y][x] = baseHex }
} }
func outside(_ x: Int, _ y: Int) -> Bool { x < 0 || y < 0 || x >= grid || y >= grid || tile[y][x] == nil }
let shape = tile
func isEdge(_ x: Int, _ y: Int) -> Bool {
    [(1, 0), (-1, 0), (0, 1), (0, -1)].contains { d in
        let nx = x + d.0, ny = y + d.1
        return nx < 0 || ny < 0 || nx >= grid || ny >= grid || shape[ny][nx] == nil
    }
}
/// Steps (up, down, left, right) to the nearest grid pixel outside the tile.
func depth(_ x: Int, _ y: Int) -> Int {
    var best = Int.max
    for oy in -1...grid { for ox in -1...grid where outside(ox, oy) {
        best = min(best, abs(x - ox) + abs(y - oy))
    } }
    return best
}
for y in 0..<grid { for x in 0..<grid where shape[y][x] != nil {
    if isEdge(x, y) { tile[y][x] = outlineHex; continue }
    guard depth(x, y) <= band else { continue }
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

// 4. The canvas: the tile's shadow, then each grid pixel as a square block.
guard let ctx = CGContext(data: nil, width: canvas, height: canvas, bitsPerComponent: 8, bytesPerRow: 0,
                          space: space, bitmapInfo: alphaInfo) else { fail("context") }
let margin = (canvas - grid * block) / 2
func blockRect(_ x: Int, _ y: Int) -> CGRect {
    // CG's origin is bottom-left; grid row 0 is the top.
    CGRect(x: margin + x * block, y: margin + (grid - 1 - y) * block, width: block, height: block)
}
ctx.setShouldAntialias(false)

let silhouette = CGMutablePath()
for y in 0..<grid { for x in 0..<grid where tile[y][x] != nil { silhouette.addRect(blockRect(x, y)) } }
ctx.saveGState()
ctx.setShadow(offset: CGSize(width: 0, height: -10), blur: 28, color: CGColor(gray: 0, alpha: 0.35))
ctx.addPath(silhouette)
ctx.setFillColor(rgb(outlineHex))
ctx.fillPath()
ctx.restoreGState()

for y in 0..<grid { for x in 0..<grid {
    guard let hex = tile[y][x] else { continue }
    ctx.setFillColor(rgb(hex))
    ctx.fill(blockRect(x, y))
} }

guard let out = ctx.makeImage(),
      let dest = CGImageDestinationCreateWithURL(outURL as CFURL, UTType.png.identifier as CFString, 1, nil) else { fail("output") }
CGImageDestinationAddImage(dest, out, nil)
guard CGImageDestinationFinalize(dest) else { fail("cannot write \(outURL.path)") }
print("master: \(outURL.path) (Bit's face on a \(grid)×\(grid) grid × \(block) px)")
