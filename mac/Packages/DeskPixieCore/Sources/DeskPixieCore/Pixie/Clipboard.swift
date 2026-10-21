import Foundation

/// What is on the clipboard that DeskPixie could attach (#240), known from its types alone.
public enum ClipboardKind: Equatable, Sendable {
    case image, files, none
}

/// The clipboard as attachments see it: NSPasteboard in the app, a fake in tests. Reading the
/// types never asks the user; reading the contents may (macOS's "Paste from Other Apps").
public protocol ClipboardSource: AnyObject {
    var changeCount: Int { get }
    /// The types on the clipboard (UTIs). Never a read.
    func detectedTypes() -> Set<String>
    /// The image as PNG, or nil (a read).
    func readImagePNG() -> Data?
    /// The copied files, or none (a read).
    func readFileURLs() -> [URL]
}

public enum ClipboardRules {
    static let fileURL = "public.file-url"
    static let images: Set<String> = ["public.png", "public.jpeg", "public.tiff", "public.heic", "com.compuserve.gif",
                                      "org.webmproject.webp"]
    static let text: Set<String> = ["public.utf8-plain-text", "public.plain-text", "public.rtf", "public.html",
                                    "com.apple.flat-rtfd", "public.utf16-plain-text"]

    /// Offered: copied files (Finder also puts their names as text); an image with no text
    /// besides its URL. Never a text copy, even one that carries an image.
    public static func kind(_ types: Set<String>) -> ClipboardKind {
        if types.contains(fileURL) { return .files }
        if !types.isDisjoint(with: text) { return .none }
        return types.isDisjoint(with: images) ? .none : .image
    }

    /// The hint row's words for it.
    public static func hint(_ kind: ClipboardKind) -> String? {
        switch kind {
        case .image: return "Screenshot copied"
        case .files: return "File copied"
        case .none: return nil
        }
    }

    /// Cmd-V in the text box attaches when the clipboard holds an image or files and no text
    /// to paste; otherwise it pastes as usual.
    public static func pasteAttaches(_ types: Set<String>) -> Bool {
        kind(types) != .none
    }
}
