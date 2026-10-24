import AppKit

/// The tooltip over a span in Pixie's bubbles (#277). AppKit shows its own tooltips only for the
/// active app, and Pixie's panels never make DeskPixie active, so this is a small panel of its
/// own: shown a moment after the pointer rests on a span, under it, and gone when it leaves.
@MainActor
final class SpanTooltip {
    static let shared = SpanTooltip()

    private let panel: NSPanel
    private let label = NSTextField(labelWithString: "")
    private var pending: DispatchWorkItem?
    /// The span the tooltip is for, in screen coordinates.
    private var anchor: NSRect = .zero
    static let delay: TimeInterval = 0.35

    private init() {
        panel = NSPanel(contentRect: .zero, styleMask: [.borderless, .nonactivatingPanel], backing: .buffered,
                        defer: true)
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = true
        panel.ignoresMouseEvents = true
        panel.level = .popUpMenu
        panel.collectionBehavior = [.canJoinAllSpaces, .transient, .ignoresCycle]
        let box = NSView()
        box.wantsLayer = true
        box.layer?.cornerRadius = 6
        box.layer?.backgroundColor = NSColor(white: 0.08, alpha: 0.96).cgColor
        box.layer?.borderColor = NSColor(white: 1, alpha: 0.15).cgColor
        box.layer?.borderWidth = 1
        label.maximumNumberOfLines = 3
        label.lineBreakMode = .byTruncatingMiddle
        label.translatesAutoresizingMaskIntoConstraints = true
        box.addSubview(label)
        panel.contentView = box
        panel.setAccessibilityElement(false)
    }

    /// Shows text for the span at rect (screen coordinates) after a moment, or at once when the
    /// tooltip already shows (the pointer moved to the next span, or "Copied").
    func show(_ text: String, for rect: NSRect) {
        pending?.cancel()
        anchor = rect
        if panel.isVisible {
            place(text)
            return
        }
        let show = DispatchWorkItem { [weak self] in self?.place(text) }
        pending = show
        DispatchQueue.main.asyncAfter(deadline: .now() + Self.delay, execute: show)
    }

    func hide() {
        pending?.cancel()
        pending = nil
        panel.orderOut(nil)
    }

    private func place(_ text: String) {
        // The text can move under a resting pointer (a card growing as a reply types out): show
        // only while the pointer is on the span.
        guard anchor.insetBy(dx: -2, dy: -2).contains(NSEvent.mouseLocation) else {
            hide()
            return
        }
        label.attributedStringValue = Self.styled(text)
        let maxWidth: CGFloat = 360
        var size = label.sizeThatFits(NSSize(width: maxWidth, height: CGFloat.greatestFiniteMagnitude))
        size.width = min(ceil(size.width), maxWidth)
        size.height = ceil(size.height)
        let pad = NSSize(width: 8, height: 4)
        label.frame = NSRect(x: pad.width, y: pad.height, width: size.width, height: size.height)
        let frame = NSSize(width: size.width + pad.width * 2, height: size.height + pad.height * 2)
        let screen = NSScreen.screens.first { $0.frame.intersects(anchor) }?.visibleFrame ?? .infinite
        // Under the span, else over it; centred on it, kept on its screen.
        var origin = NSPoint(x: anchor.midX - frame.width / 2, y: anchor.minY - 6 - frame.height)
        if origin.y < screen.minY { origin.y = anchor.maxY + 6 }
        origin.x = max(screen.minX + 4, min(origin.x, screen.maxX - frame.width - 4))
        panel.setFrame(NSRect(origin: origin, size: frame), display: true)
        panel.orderFrontRegardless()
    }

    /// The first line is what a click does; the rest (an address, ⌥-click) is quieter.
    private static func styled(_ text: String) -> NSAttributedString {
        let lines = text.split(separator: "\n", maxSplits: 1).map(String.init)
        let out = NSMutableAttributedString(string: lines.first ?? "", attributes: [
            .font: NSFont.systemFont(ofSize: 11, weight: .semibold), .foregroundColor: NSColor.white])
        if lines.count > 1 {
            out.append(NSAttributedString(string: "\n" + lines[1], attributes: [
                .font: NSFont.systemFont(ofSize: 11), .foregroundColor: NSColor(white: 1, alpha: 0.65)]))
        }
        return out
    }
}
