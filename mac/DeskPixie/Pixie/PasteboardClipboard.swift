import AppKit
import DeskPixieCore

/// The general pasteboard as attachments see it (#240). Its types are metadata (never a
/// prompt); reading its contents is what macOS may ask about.
final class PasteboardClipboard: ClipboardSource {
    private let board = NSPasteboard.general

    var changeCount: Int { board.changeCount }

    func detectedTypes() -> Set<String> { Set((board.types ?? []).map(\.rawValue)) }

    func readImagePNG() -> Data? {
        if let png = board.data(forType: .png) { return png }
        guard let image = NSImage(pasteboard: board), let tiff = image.tiffRepresentation,
              let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .png, properties: [:])
    }

    func readFileURLs() -> [URL] {
        board.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL] ?? []
    }
}
