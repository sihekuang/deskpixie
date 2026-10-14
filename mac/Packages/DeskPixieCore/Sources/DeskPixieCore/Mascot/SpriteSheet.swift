import CoreGraphics
import Foundation
import ImageIO

/// A decoded sprite sheet. Keeps the RGBA bytes for per-pixel hit tests and caches crops.
/// Coordinates are sheet pixels with a top-left origin (the pack's convention).
public final class SpriteSheet: @unchecked Sendable {
    public let image: CGImage
    public let width: Int
    public let height: Int
    /// Premultiplied RGBA, row 0 = top row of the image. Immutable after init.
    private let alpha: [UInt8]
    private let lock = NSLock()
    private var crops: [CropKey: CGImage] = [:]

    private struct CropKey: Hashable {
        let x, y, w, h: Int
    }

    public init(image: CGImage) {
        self.image = image
        let width = image.width, height = image.height
        self.width = width
        self.height = height
        var rgba = [UInt8](repeating: 0, count: width * height * 4)
        rgba.withUnsafeMutableBytes { buf in
            guard let ctx = CGContext(data: buf.baseAddress, width: width, height: height, bitsPerComponent: 8,
                                      bytesPerRow: width * 4, space: CGColorSpaceCreateDeviceRGB(),
                                      bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return }
            ctx.clear(CGRect(x: 0, y: 0, width: width, height: height))
            ctx.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        }
        alpha = stride(from: 3, to: rgba.count, by: 4).map { rgba[$0] }
    }

    public convenience init?(url: URL) {
        guard let src = CGImageSourceCreateWithURL(url as CFURL, nil),
              let img = CGImageSourceCreateImageAtIndex(src, 0, nil) else { return nil }
        self.init(image: img)
    }

    /// The sub-image at `r` (sheet pixels, top-left origin), cached per rect; nil outside the sheet.
    public func crop(_ r: CGRect) -> CGImage? {
        let key = CropKey(x: Int(r.minX), y: Int(r.minY), w: Int(r.width), h: Int(r.height))
        lock.lock()
        defer { lock.unlock() }
        if let c = crops[key] { return c }
        guard key.w > 0, key.h > 0, key.x >= 0, key.y >= 0,
              key.x + key.w <= width, key.y + key.h <= height,
              let c = image.cropping(to: CGRect(x: key.x, y: key.y, width: key.w, height: key.h)) else { return nil }
        crops[key] = c
        return c
    }

    /// True when the sheet pixel containing `p` has alpha > 0; false outside the sheet.
    public func isOpaque(sheetPoint p: CGPoint) -> Bool {
        guard p.x >= 0, p.y >= 0 else { return false }
        let x = Int(p.x.rounded(.down)), y = Int(p.y.rounded(.down))
        guard x < width, y < height else { return false }
        return alpha[y * width + x] > 0
    }
}
