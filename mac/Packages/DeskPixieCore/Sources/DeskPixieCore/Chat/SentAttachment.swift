import CoreGraphics
import Foundation

/// A file a message of yours went with (#394): a picture shows as a thumbnail under the bubble,
/// anything else as its icon and name; a click opens either in Quick Look.
public enum SentAttachment {
    /// What the core counts as an image (`core/attach` kindOf).
    static let imageExtensions: Set<String> = ["png", "jpg", "jpeg", "gif", "webp"]

    /// The largest a thumbnail is drawn.
    public static let maxThumbnail = CGSize(width: 220, height: 140)

    public static func isImage(_ path: String) -> Bool {
        imageExtensions.contains((path as NSString).pathExtension.lowercased())
    }

    /// The picture scaled to fit `bound`, keeping its shape and never enlarged; nil for a
    /// picture with no size.
    public static func thumbnailSize(_ image: CGSize, in bound: CGSize = maxThumbnail) -> CGSize? {
        guard image.width > 0, image.height > 0 else { return nil }
        let scale = min(1, bound.width / image.width, bound.height / image.height)
        return CGSize(width: (image.width * scale).rounded(), height: (image.height * scale).rounded())
    }
}
