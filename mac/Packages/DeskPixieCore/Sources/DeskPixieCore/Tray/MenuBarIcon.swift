import AppKit
import CoreGraphics

/// The menu bar item: Pixie's face (`PixieFace`: idle frame 0 with the closed mouth) as an 18 pt
/// template image, so macOS tints it for light and dark menu bars. Template images use alpha
/// only, so the face's features are cut out of the silhouette to keep it readable: inner pixels
/// that stand out from the body's usual brightness (eyes on a light body, or on a dark one). The
/// silhouette's edge always stays, so outlines and thin parts (an antenna, eye stalks) survive.
/// Scaled nearest-neighbour; `sparkles` when the pack cannot be read.
public enum MenuBarIcon {
    public static let pointSize: CGFloat = 18
    public static let tooltip = "DeskPixie"
    public static let fallbackSymbol = "sparkles"
    /// How far (0…1) a pixel's luminance must be from the body's median to count as a feature.
    static let featureContrast: CGFloat = 0.3
    /// Below this share of Pixie's pixels left after cutting, the plain silhouette is used: a
    /// pixie that is mostly "features" would fall apart into specks.
    static let minKeptShare = 0.6
    /// Pixels at least this opaque are Pixie; fainter ones (a soft shadow or glow) are left out,
    /// so they neither fill the template nor count as its edge.
    static let solidAlpha: UInt8 = 128

    /// Black-on-clear mask of Pixie's face, trimmed to its pixels; nil when the pack has no
    /// idle state, the sheet cannot be cropped or the frame has no solid pixels.
    public static func faceMask(pack: Pack, sheet: SpriteSheet) -> CGImage? {
        guard let face = PixieFace.render(pack: pack, sheet: sheet) else { return nil }
        let w = face.width, h = face.height
        var lum = [CGFloat?](repeating: nil, count: w * h)
        for y in 0..<h { for x in 0..<w where face.alpha(x, y) >= solidAlpha { lum[y * w + x] = face.luminance(x, y) } }
        let opaque = lum.compactMap { $0 }.sorted()
        guard !opaque.isEmpty else { return nil }
        let body = opaque[opaque.count / 2]
        func filled(_ x: Int, _ y: Int) -> Bool { x >= 0 && y >= 0 && x < w && y < h && lum[y * w + x] != nil }

        let silhouette = lum.map { $0 != nil }
        var cut = silhouette
        for y in 0..<h { for x in 0..<w {
            guard let l = lum[y * w + x], abs(l - body) >= featureContrast else { continue }
            // Only inner pixels: the edge keeps the outline.
            if filled(x - 1, y), filled(x + 1, y), filled(x, y - 1), filled(x, y + 1) { cut[y * w + x] = false }
        } }
        let kept = cut.filter { $0 }.count
        let on = Double(kept) >= minKeptShare * Double(opaque.count) ? cut : silhouette

        // Cutting never touches the edge, so the mask's bounds are the silhouette's.
        var minX = w, minY = h, maxX = -1, maxY = -1
        for y in 0..<h { for x in 0..<w where silhouette[y * w + x] {
            minX = min(minX, x); maxX = max(maxX, x); minY = min(minY, y); maxY = max(maxY, y)
        } }
        let b = (x: minX, y: minY, width: maxX - minX + 1, height: maxY - minY + 1)
        var out = [UInt8](repeating: 0, count: b.width * b.height * 4)
        for y in 0..<b.height {
            for x in 0..<b.width where on[(y + b.y) * w + x + b.x] {
                out[(y * b.width + x) * 4 + 3] = 255
            }
        }
        guard let provider = CGDataProvider(data: Data(out) as CFData) else { return nil }
        return CGImage(width: b.width, height: b.height, bitsPerComponent: 8, bitsPerPixel: 32, bytesPerRow: b.width * 4,
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
