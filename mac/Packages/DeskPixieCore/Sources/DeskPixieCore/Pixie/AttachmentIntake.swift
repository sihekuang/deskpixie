import Foundation

/// What the shell hands the core's attachments.add (#240): image bytes with a name, or a file path.
public enum AttachmentAdd: Equatable, Sendable {
    case data(name: String, data: Data)
    case file(String)
}

public enum IntakeResult: Equatable, Sendable {
    case adds([AttachmentAdd])
    /// Nothing to add, and why (the words the user sees, or Pixie says).
    case refused(String)
}

/// Turns the clipboard or a drop into attachments: images shrunk to 2 MB (as PNG, or JPEG when
/// that's what fits), files by path (the core copies them and checks their size).
public enum AttachmentIntake {
    public static let maxBytes = 2 << 20
    public static let nothing = "There's no image or file on the clipboard."
    public static let notAllowed = "DeskPixie isn't allowed to read the clipboard. Allow it in System Settings, Privacy & Security, Paste from Other Apps."
    public static let badImage = "Couldn't read that image."

    /// Reads the clipboard (it may ask the user, once) only when it holds something to attach.
    public static func fromClipboard(_ clip: ClipboardSource, now: Date = Date()) -> IntakeResult {
        switch ClipboardRules.kind(clip.detectedTypes()) {
        case .none:
            return .refused(nothing)
        case .files:
            let files = clip.readFileURLs().filter(\.isFileURL)
            return files.isEmpty ? .refused(notAllowed) : .adds(files.map { .file($0.path) })
        case .image:
            guard let png = clip.readImagePNG() else { return .refused(notAllowed) }
            return fromImage(png, now: now)
        }
    }

    /// An image's bytes (copied, pasted or dropped) as a screenshot attachment.
    public static func fromImage(_ data: Data, now: Date = Date()) -> IntakeResult {
        guard let fit = ImageShrink.fit(data, maxBytes: maxBytes) else { return .refused(badImage) }
        return .adds([.data(name: "\(screenshotName(now)).\(fit.ext)", data: fit.data)])
    }

    /// "Screenshot 14.32" (a colon is a path separator in Finder).
    static func screenshotName(_ now: Date) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.dateFormat = "HH.mm.ss"
        return "Screenshot \(f.string(from: now))"
    }
}
