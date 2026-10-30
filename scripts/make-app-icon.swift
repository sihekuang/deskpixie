// Renders DeskPixie's 1024×1024 app icon master from Bit's sprite sheet.
// Run via scripts/make-app-icon.sh:  swift make-app-icon.swift <pack dir> <out.png>
//
// The art is Bit's idle frame 0 with the closed mouth composited at the pack's mouth anchor —
// the same face MenuBarIcon uses — trimmed to its opaque pixels, scaled up nearest-neighbour by
// the largest whole factor that fits the macOS icon grid (an 824×824 square, 100 px margin), and
// centred on a transparent canvas: no tile behind Bit.
import AppKit
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

let canvas = 1024
/// The macOS icon grid: Bit stands as tall as other apps' icons.
let tile = CGRect(x: 100, y: 100, width: 824, height: 824)

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
guard let face = cell.makeImage() else { fail("cell image") }

// 2. Trim to opaque pixels (memory row 0 is the top of the image).
let data = cell.data!.assumingMemoryBound(to: UInt8.self)
let stride = cell.bytesPerRow
var minX = w, minY = h, maxX = -1, maxY = -1
for y in 0..<h { for x in 0..<w where data[y * stride + x * 4 + 3] > 0 {
    minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
} }
guard maxX >= minX else { fail("empty frame") }
let tw = maxX - minX + 1, th = maxY - minY + 1
guard let art = face.cropping(to: CGRect(x: minX, y: minY, width: tw, height: th)) else { fail("trim") }

// 3. The canvas, transparent.
guard let ctx = CGContext(data: nil, width: canvas, height: canvas, bitsPerComponent: 8, bytesPerRow: 0,
                          space: space, bitmapInfo: alphaInfo) else { fail("context") }

// Largest whole scale that fits, so every sprite pixel is an exact square block.
let room = tile.width
let scale = max(1, Int(min(room / CGFloat(tw), room / CGFloat(th))))
let aw = tw * scale, ah = th * scale
let ax = (canvas - aw) / 2, ay = (canvas - ah) / 2
ctx.interpolationQuality = .none
ctx.setShouldAntialias(false)
ctx.draw(art, in: CGRect(x: ax, y: ay, width: aw, height: ah))

guard let out = ctx.makeImage(),
      let dest = CGImageDestinationCreateWithURL(outURL as CFURL, UTType.png.identifier as CFString, 1, nil) else { fail("output") }
CGImageDestinationAddImage(dest, out, nil)
guard CGImageDestinationFinalize(dest) else { fail("cannot write \(outURL.path)") }
print("master: \(outURL.path) (Bit \(tw)×\(th) px × \(scale))")
