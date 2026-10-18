import AppKit
import CoreGraphics

/// The menu bar item: Pixie's face (idle frame 0 with the closed mouth) as an 18 pt template
/// image, so macOS tints it for light and dark menu bars. Template images use alpha only, so
/// the face's dark pixels (eyes, mouth, outline) are cut out of the silhouette to keep it
/// readable. Scaled nearest-neighbour; `sparkles` when the pack cannot be read.
public enum MenuBarIcon {
    public static let pointSize: CGFloat = 18
    public static let tooltip = "DeskPixie"
    public static let fallbackSymbol = "sparkles"
    /// Below this luminance (0…1, unpremultiplied) a pixel is a feature, not body.
    static let featureLuminance: CGFloat = 0.2

    /// Black-on-clear mask of Pixie's face, trimmed to its opaque pixels; nil when the pack has
    /// no idle state or the sheet cannot be cropped.
    public static func faceMask(pack: Pack, sheet: SpriteSheet) -> CGImage? {
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
                // CG's origin is bottom-left; the anchor is top-left in cell pixels.
                ctx.draw(mouth, in: CGRect(x: m.anchor[0], y: h - m.anchor[1] - mh, width: mw, height: mh))
            }
            return true
        }
        guard drawn else { return nil }

        var on = [Bool](repeating: false, count: w * h)
        var minX = w, minY = h, maxX = -1, maxY = -1
        for i in 0..<(w * h) {
            let a = rgba[i * 4 + 3]
            guard a > 0 else { continue }
            // Track Pixie's whole extent (features included) so the face stays centred.
            let x = i % w, y = i / w
            minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
            let k = 255 / CGFloat(a)
            let lum = (0.2126 * CGFloat(rgba[i * 4]) + 0.7152 * CGFloat(rgba[i * 4 + 1])
                + 0.0722 * CGFloat(rgba[i * 4 + 2])) * k / 255
            on[i] = lum >= featureLuminance
        }
        guard maxX >= minX, maxY >= minY else { return nil }
        // Trim to the body pixels left after cutting (an outline-only edge would be empty).
        var bx0 = maxX, by0 = maxY, bx1 = minX, by1 = minY
        for y in minY...maxY {
            for x in minX...maxX where on[y * w + x] {
                bx0 = min(bx0, x); bx1 = max(bx1, x); by0 = min(by0, y); by1 = max(by1, y)
            }
        }
        guard bx1 >= bx0, by1 >= by0 else { return nil }
        let tw = bx1 - bx0 + 1, th = by1 - by0 + 1
        var out = [UInt8](repeating: 0, count: tw * th * 4)
        for y in 0..<th {
            for x in 0..<tw where on[(y + by0) * w + x + bx0] {
                out[(y * tw + x) * 4 + 3] = 255
            }
        }
        guard let provider = CGDataProvider(data: Data(out) as CFData) else { return nil }
        return CGImage(width: tw, height: th, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: tw * 4,
                       space: CGColorSpaceCreateDeviceRGB(),
                       bitmapInfo: CGBitmapInfo(rawValue: CGImageAlphaInfo.premultipliedLast.rawValue),
                       provider: provider, decode: nil, shouldInterpolate: false, intent: .defaultIntent)
    }

    /// The 18×18 pt template image (the face fitted and centred, nearest-neighbour).
    public static func image(pack: Pack, sheet: SpriteSheet) -> NSImage? {
        guard let mask = faceMask(pack: pack, sheet: sheet) else { return nil }
        let side = pointSize
        let scale = min(side / CGFloat(mask.width), side / CGFloat(mask.height))
        let size = CGSize(width: CGFloat(mask.width) * scale, height: CGFloat(mask.height) * scale)
        let rect = CGRect(x: (side - size.width) / 2, y: (side - size.height) / 2, width: size.width, height: size.height)
        let img = NSImage(size: NSSize(width: side, height: side), flipped: false) { _ in
            guard let ctx = NSGraphicsContext.current else { return false }
            ctx.imageInterpolation = .none
            ctx.cgContext.interpolationQuality = .none
            ctx.cgContext.draw(mask, in: rect)
            return true
        }
        img.isTemplate = true
        img.accessibilityDescription = tooltip
        return img
    }

    /// From a pack folder (`PackFolder`, e.g. the bundled `pixies/bit`).
    public static func load(packDirectory dir: URL) -> NSImage? {
        guard let (pack, sheet) = PackFolder.load(dir) else { return nil }
        return image(pack: pack, sheet: sheet)
    }

    /// Pixie's face, else the `sparkles` symbol (also a template).
    public static func imageOrFallback(packDirectory dir: URL) -> NSImage {
        if let img = load(packDirectory: dir) { return img }
        let img = NSImage(systemSymbolName: fallbackSymbol, accessibilityDescription: tooltip) ?? NSImage()
        img.isTemplate = true
        return img
    }
}
