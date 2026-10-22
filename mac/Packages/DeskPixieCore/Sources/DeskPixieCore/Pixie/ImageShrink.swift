import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

/// Images for attachments (#240): kept as they are when Claude reads them and they fit;
/// otherwise converted to PNG, then scaled down (and tried as JPEG) until they fit.
public enum ImageShrink {
    private static let kept: [String: String] = [
        UTType.png.identifier: "png", UTType.jpeg.identifier: "jpg", UTType.gif.identifier: "gif",
        UTType.webP.identifier: "webp",
    ]

    /// The image as data of at most maxBytes, with its file extension; nil if data isn't an image.
    public static func fit(_ data: Data, maxBytes: Int) -> (data: Data, ext: String)? {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil),
              let type = CGImageSourceGetType(src) as String?,
              let image = CGImageSourceCreateImageAtIndex(src, 0, nil) else { return nil }
        if let ext = kept[type], data.count <= maxBytes { return (data, ext) }
        var scale = 1.0
        for _ in 0..<12 {
            guard let scaled = scale < 1 ? resize(image, scale) : image else { return nil }
            if let png = encode(scaled, UTType.png, quality: nil), png.count <= maxBytes { return (png, "png") }
            if let jpg = encode(scaled, UTType.jpeg, quality: 0.85), jpg.count <= maxBytes { return (jpg, "jpg") }
            scale *= 0.75
        }
        return nil
    }

    private static func resize(_ image: CGImage, _ scale: Double) -> CGImage? {
        let w = max(1, Int(Double(image.width) * scale)), h = max(1, Int(Double(image.height) * scale))
        guard let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8, bytesPerRow: 0,
                                  space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return nil }
        ctx.interpolationQuality = .high
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
        return ctx.makeImage()
    }

    private static func encode(_ image: CGImage, _ type: UTType, quality: Double?) -> Data? {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(out, type.identifier as CFString, 1, nil) else { return nil }
        let props = quality.map { [kCGImageDestinationLossyCompressionQuality: $0] as CFDictionary }
        CGImageDestinationAddImage(dest, image, props)
        return CGImageDestinationFinalize(dest) ? out as Data : nil
    }
}
