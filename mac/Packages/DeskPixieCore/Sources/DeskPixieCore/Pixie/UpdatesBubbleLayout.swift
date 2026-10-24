import AppKit

/// How wide the updates bubble is (#105, #278): as wide as its longest line needs on one line,
/// from `minWidth` up to `maxWidth`, where lines wrap: an ordinary line to at most `maxLines`
/// lines, a question in full (nothing a question shows is cut).
public enum UpdatesBubbleLayout {
    public static let minWidth: CGFloat = 240
    public static let maxWidth: CGFloat = 440
    /// The most lines an ordinary line shows; a question shows all of it.
    public static let maxLines = 3

    /// The width the lines need: their longest text on one line, plus the row's padding.
    public static func width(for lines: [UpdateLine]) -> CGFloat {
        let callout = NSFont.preferredFont(forTextStyle: .callout)
        let small = NSFont.systemFont(ofSize: NSFont.smallSystemFontSize)
        let caption = NSFont.preferredFont(forTextStyle: .caption1).pointSize
        let title = NSFont.systemFont(ofSize: caption, weight: .semibold)
        let mono = NSFont.monospacedSystemFont(ofSize: caption, weight: .regular)
        func one(_ s: String, _ f: NSFont) -> CGFloat { (s as NSString).size(withAttributes: [.font: f]).width }
        var need: CGFloat = 0
        for l in lines {
            guard l.kind == "question" else {
                // row and bubble padding
                need = max(need, one(l.shownText, callout) + 28)
                if let t = l.title { need = max(need, one(t, title) + 28) }
                continue
            }
            need = max(need, one(l.shownText, callout) + 48)                       // icon, row and bubble padding
            if let t = l.title { need = max(need, one(t, title) + 48) }
            for o in l.options ?? [] { need = max(need, one(o, small) + 52) } // button and bubble padding
            for d in l.details ?? [] { need = max(need, one(d, small) + 52) }
            for n in (l.note ?? "").split(separator: "\n") { need = max(need, one(String(n), mono) + 48) }
        }
        return min(max(ceil(need), minWidth), maxWidth)
    }
}
