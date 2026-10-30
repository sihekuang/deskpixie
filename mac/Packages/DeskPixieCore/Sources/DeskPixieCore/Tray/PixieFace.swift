import CoreGraphics
import Foundation

/// Pixie at rest, as the icons show it: idle frame 0 with the closed mouth composited where the
/// pixie on screen has it (the anchor moved by that frame's faceDy), in cell pixels. Any pack that loads has both (`idle` and `mouth` are
/// required), so this works for custom pixies of any cell size.
public struct PixieFace: Equatable {
    public let width: Int
    public let height: Int
    /// Premultiplied RGBA, row 0 at the top.
    public let rgba: [UInt8]

    /// nil when the pack has no idle state or the sheet cannot be cropped.
    public static func render(pack: Pack, sheet: SpriteSheet) -> PixieFace? {
        let w = pack.cellWidth, h = pack.cellHeight
        guard w > 0, h > 0, let idle = pack.states["idle"],
              let body = sheet.crop(CGRect(x: 0, y: idle.row * h, width: w, height: h)) else { return nil }
        var rgba = [UInt8](repeating: 0, count: w * h * 4)
        let drawn: Bool = rgba.withUnsafeMutableBytes { buf in
            guard let ctx = CGContext(data: buf.baseAddress, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                      space: CGColorSpaceCreateDeviceRGB(),
                                      bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            ctx.interpolationQuality = .none
            ctx.draw(body, in: CGRect(x: 0, y: 0, width: w, height: h))
            let m = pack.mouth
            let mw = m.size[0], mh = m.size[1]
            if m.levels > 0, let mouth = sheet.crop(CGRect(x: 0, y: m.row * h, width: mw, height: mh)) {
                let at = PixiePlayer(state: "idle").mouthPos(pack: pack)
                // CG's origin is bottom-left; the anchor is top-left in cell pixels.
                ctx.draw(mouth, in: CGRect(x: at.x, y: CGFloat(h) - at.y - CGFloat(mh), width: CGFloat(mw), height: CGFloat(mh)))
            }
            return true
        }
        return drawn ? PixieFace(width: w, height: h, rgba: rgba) : nil
    }

    public func alpha(_ x: Int, _ y: Int) -> UInt8 { rgba[(y * width + x) * 4 + 3] }

    /// Relative luminance 0…1 of an opaque pixel (unpremultiplied); nil when it is clear.
    public func luminance(_ x: Int, _ y: Int) -> CGFloat? {
        let i = (y * width + x) * 4
        let a = rgba[i + 3]
        guard a > 0 else { return nil }
        let k = 255 / CGFloat(a)
        return (0.2126 * CGFloat(rgba[i]) + 0.7152 * CGFloat(rgba[i + 1]) + 0.0722 * CGFloat(rgba[i + 2])) * k / 255
    }

    /// The smallest rectangle holding every opaque pixel (top-left origin); nil when all clear.
    public var opaqueBounds: (x: Int, y: Int, width: Int, height: Int)? {
        var minX = width, minY = height, maxX = -1, maxY = -1
        for y in 0..<height { for x in 0..<width where alpha(x, y) > 0 {
            minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
        } }
        guard maxX >= minX else { return nil }
        return (minX, minY, maxX - minX + 1, maxY - minY + 1)
    }

    /// The face as an image, cropped to `opaqueBounds`; nil when all clear.
    public func trimmedImage() -> CGImage? {
        guard let b = opaqueBounds else { return nil }
        var out = [UInt8](repeating: 0, count: b.width * b.height * 4)
        for y in 0..<b.height {
            let src = ((b.y + y) * width + b.x) * 4
            out.replaceSubrange(y * b.width * 4..<(y + 1) * b.width * 4, with: rgba[src..<src + b.width * 4])
        }
        guard let provider = CGDataProvider(data: Data(out) as CFData) else { return nil }
        return CGImage(width: b.width, height: b.height, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: b.width * 4,
                       space: CGColorSpaceCreateDeviceRGB(),
                       bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
                       provider: provider, decode: nil, shouldInterpolate: false, intent: .defaultIntent)
    }
}
