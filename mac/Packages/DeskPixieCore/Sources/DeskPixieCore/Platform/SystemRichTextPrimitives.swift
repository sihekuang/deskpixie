import AppKit

/// The Mac's side of a click on a span in Pixie's bubbles (#277).
public struct SystemRichTextPrimitives: RichTextPrimitives {
    public init() {}

    public func canOpen(_ url: URL) -> Bool {
        url.isFileURL || NSWorkspace.shared.urlForApplication(toOpen: url) != nil
    }

    public func open(_ url: URL) -> Bool {
        NSWorkspace.shared.open(url)
    }

    public func reveal(_ path: String) {
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }

    public func copy(_ text: String) {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
    }
}
