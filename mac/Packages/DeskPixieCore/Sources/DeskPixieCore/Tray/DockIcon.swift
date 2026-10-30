import AppKit
import CoreGraphics

/// The Dock icon while DeskPixie runs: the chosen pixie's face (`PixieFace`) on a transparent
/// 1024 px canvas, as tall as other apps' icons, like the bundled icon
/// (scripts/make-app-icon.swift draws Bit the same way). Pixel art is scaled up nearest-neighbour
/// by the largest whole factor that fits the macOS icon grid; a custom pixie drawn bigger than the
/// grid is scaled down to fit. Finder and Launchpad keep the bundled icon.
public enum DockIcon {
    public static let canvas = 1024
    /// The macOS icon grid: an 824 px square with a 100 px margin.
    public static let grid = 824

    /// Where the face goes on the canvas (bottom-left origin), centred; nil for an empty face.
    public static func artRect(width: Int, height: Int) -> CGRect? {
        guard width > 0, height > 0 else { return nil }
        let fit = CGFloat(grid) / CGFloat(max(width, height))
        let scale = fit >= 1 ? fit.rounded(.down) : fit
        let w = CGFloat(width) * scale, h = CGFloat(height) * scale
        return CGRect(x: ((CGFloat(canvas) - w) / 2).rounded(.down), y: ((CGFloat(canvas) - h) / 2).rounded(.down),
                      width: w, height: h)
    }

    /// The 1024 px bitmap; nil when the pack's face cannot be drawn.
    public static func cgImage(pack: Pack, sheet: SpriteSheet) -> CGImage? {
        guard let face = PixieFace.render(pack: pack, sheet: sheet), let art = face.trimmedImage(),
              let rect = artRect(width: art.width, height: art.height),
              let ctx = CGContext(data: nil, width: canvas, height: canvas, bitsPerComponent: 8, bytesPerRow: 0,
                                  space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.interpolationQuality = .none
        ctx.setShouldAntialias(false)
        ctx.draw(art, in: rect)
        return ctx.makeImage()
    }

    /// For `NSApp.applicationIconImage`.
    public static func image(pack: Pack, sheet: SpriteSheet) -> NSImage? {
        guard let cg = cgImage(pack: pack, sheet: sheet) else { return nil }
        let img = NSImage(cgImage: cg, size: NSSize(width: canvas, height: canvas))
        img.accessibilityDescription = MenuBarIcon.tooltip
        return img
    }
}
