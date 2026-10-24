import AppKit
import DeskPixieCore
import SwiftUI

/// A message in Pixie's bubbles with its links, files, code, commits and emails clickable
/// (#277). The spans come from the core (`RichTextStore`); until they do, the plain text shows.
/// `shown` is the part typed out so far (the reply card), nil when all of it shows.
struct RichMessage: View {
    @EnvironmentObject private var store: RichTextStore
    let text: String
    var shown: String? = nil
    var sessions: [String] = []
    var font: NSFont = .preferredFont(forTextStyle: .callout)
    /// 0 = no limit; else the text is cut with "…" after this many lines.
    var maxLines = 0
    /// Plain text can be selected (the reply card); off, a click on it goes to what is behind
    /// (a bubble row's button).
    var selectable = false

    var body: some View {
        let _ = store.version
        let spans = store.spans(text, sessions: sessions) ?? [Self.plain(text)]
        RichTextView(spans: shown.map { RichText.prefix(spans, shown: $0) } ?? spans, font: font, maxLines: maxLines,
                     selectable: selectable)
    }

    static func plain(_ text: String) -> RichSpan {
        RichSpan(kind: "text", text: text, raw: text, bold: false, url: nil, path: nil, dir: false, line: 0, col: 0,
                 open: [])
    }
}

struct RichTextView: NSViewRepresentable {
    let spans: [RichSpan]
    let font: NSFont
    let maxLines: Int
    let selectable: Bool

    func makeNSView(context: Context) -> SpanTextView {
        SpanTextView(actions: RichTextActions(os: SystemRichTextPrimitives()))
    }

    func updateNSView(_ view: SpanTextView, context: Context) {
        view.show(spans, font: font, maxLines: maxLines, selectable: selectable)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView: SpanTextView, context: Context) -> CGSize? {
        let width = proposal.width.flatMap { $0.isFinite && $0 > 0 ? $0 : nil }
        let used = nsView.fitting(width: width ?? 10_000)
        return CGSize(width: width ?? used.width, height: used.height)
    }
}

/// The text view behind `RichTextView`: hover highlights an actionable span and shows the
/// pointing hand and its tooltip (SpanTooltip), a click opens or copies it (⌥-click shows a
/// file in Finder), and a copy flashes green with "Copied".
final class SpanTextView: NSTextView {
    private let actions: RichTextActions
    private var spans: [RichSpan] = []
    private var selectableText = false
    private var hovered: NSRange?
    private var copied: (range: NSRange, index: Int)?
    private var copiedReset: DispatchWorkItem?
    private var tracking: NSTrackingArea?
    /// What the text view shows now, to skip setting it again unchanged.
    private var current: (spans: [RichSpan], font: NSFont, maxLines: Int)?

    init(actions: RichTextActions) {
        self.actions = actions
        let storage = NSTextStorage()
        let layout = NSLayoutManager()
        storage.addLayoutManager(layout)
        let container = NSTextContainer(size: NSSize(width: 10_000, height: CGFloat.greatestFiniteMagnitude))
        container.lineFragmentPadding = 0
        // The width is set from the frame SwiftUI gives (setFrameSize, fitting), never tracked:
        // a text view that starts at width 0 would otherwise lay out at no width at all.
        container.widthTracksTextView = false
        container.heightTracksTextView = false
        layout.addTextContainer(container)
        super.init(frame: .zero, textContainer: container)
        isEditable = false
        isSelectable = true
        drawsBackground = false
        textContainerInset = .zero
        isVerticallyResizable = false
        isHorizontallyResizable = false
        isRichText = true
        setAccessibilityRole(.staticText)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    func show(_ spans: [RichSpan], font: NSFont, maxLines: Int, selectable: Bool) {
        selectableText = selectable
        isSelectable = selectable
        if let c = current, c.spans == spans, c.font == font, c.maxLines == maxLines { return }
        current = (spans, font, maxLines)
        self.spans = spans
        clearHover()
        copiedReset?.cancel()
        copied = nil
        textStorage?.setAttributedString(RichText.attributed(spans, font: font, color: .labelColor))
        textContainer?.maximumNumberOfLines = maxLines
        textContainer?.lineBreakMode = maxLines > 0 ? .byTruncatingTail : .byWordWrapping
        setAccessibilityValue(textStorage?.string)
        invalidateIntrinsicContentSize()
    }

    /// The text's size laid out at width.
    func fitting(width: CGFloat) -> CGSize {
        guard let container = textContainer, let layout = layoutManager else { return .zero }
        if container.containerSize.width != width {
            container.containerSize = NSSize(width: width, height: CGFloat.greatestFiniteMagnitude)
        }
        layout.ensureLayout(for: container)
        let used = layout.usedRect(for: container)
        return CGSize(width: ceil(used.width), height: ceil(used.height))
    }

    override func setFrameSize(_ newSize: NSSize) {
        super.setFrameSize(newSize)
        if newSize.width > 0 {
            textContainer?.containerSize = NSSize(width: newSize.width, height: CGFloat.greatestFiniteMagnitude)
        }
    }

    // MARK: Finding the span under the pointer

    /// The actionable span at a point in this view, and its range in the text.
    private func span(at point: NSPoint) -> (index: Int, range: NSRange)? {
        guard let layout = layoutManager, let container = textContainer, let storage = textStorage,
              storage.length > 0 else { return nil }
        let glyph = layout.glyphIndex(for: point, in: container)
        let rect = layout.boundingRect(forGlyphRange: NSRange(location: glyph, length: 1), in: container)
        guard rect.insetBy(dx: -1, dy: -1).contains(point) else { return nil }
        let char = layout.characterIndexForGlyph(at: glyph)
        guard char < storage.length else { return nil }
        var range = NSRange()
        guard let i = storage.attribute(RichText.spanKey, at: char, effectiveRange: &range) as? Int,
              spans.indices.contains(i) else { return nil }
        return (i, range)
    }

    private func point(of event: NSEvent) -> NSPoint { convert(event.locationInWindow, from: nil) }

    /// Off a span, an unselectable text lets the click through to what is behind it.
    override func hitTest(_ point: NSPoint) -> NSView? {
        guard let hit = super.hitTest(point) else { return nil }
        if selectableText { return hit }
        return span(at: convert(point, from: superview)) == nil ? nil : hit
    }

    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    override func viewWillMove(toWindow newWindow: NSWindow?) {
        clearHover()
        NotificationCenter.default.removeObserver(self)
        super.viewWillMove(toWindow: newWindow)
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        // The panel moving or resizing moves the text under a resting pointer: its highlight no
        // longer marks what the pointer is on.
        for name in [NSWindow.didMoveNotification, NSWindow.didResizeNotification] {
            NotificationCenter.default.addObserver(self, selector: #selector(windowChanged), name: name, object: window)
        }
    }

    @objc private func windowChanged(_ note: Notification) { clearHover() }

    // MARK: Hover

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        if let tracking { removeTrackingArea(tracking) }
        let area = NSTrackingArea(rect: .zero, options: [.mouseMoved, .mouseEnteredAndExited, .activeAlways,
                                                          .inVisibleRect, .cursorUpdate], owner: self)
        addTrackingArea(area)
        tracking = area
    }

    override func mouseMoved(with event: NSEvent) {
        let hit = span(at: point(of: event))
        if hit?.range != hovered {
            clearHover()
            if let hit { highlight(hit.index, hit.range) }
        }
        (hit == nil ? (selectableText ? NSCursor.iBeam : NSCursor.arrow) : NSCursor.pointingHand).set()
    }

    override func mouseExited(with event: NSEvent) {
        clearHover()
        NSCursor.arrow.set()
    }

    override func cursorUpdate(with event: NSEvent) {
        if hovered != nil { NSCursor.pointingHand.set() } else { super.cursorUpdate(with: event) }
    }

    private func highlight(_ index: Int, _ range: NSRange) {
        guard let layout = layoutManager else { return }
        hovered = range
        var a: [NSAttributedString.Key: Any]
        switch spans[index].kind {
        case "link":
            a = [.backgroundColor: NSColor.linkColor.withAlphaComponent(0.15),
                 .underlineColor: NSColor.linkColor]
        case "path":
            a = [.backgroundColor: NSColor.labelColor.withAlphaComponent(0.15), .foregroundColor: NSColor.linkColor,
                 .underlineStyle: NSUnderlineStyle.single.rawValue, .underlineColor: NSColor.linkColor]
        default:
            a = [.backgroundColor: NSColor.labelColor.withAlphaComponent(0.2)]
        }
        if copied?.range == range { a[.backgroundColor] = Self.copiedColor }
        layout.addTemporaryAttributes(a, forCharacterRange: range)
        SpanTooltip.shared.show(spans[index].tooltip(copied: copied?.range == range), for: screenRect(of: range))
    }

    /// Where a range of the text is on screen.
    private func screenRect(of range: NSRange) -> NSRect {
        guard let layout = layoutManager, let container = textContainer, let window else { return .zero }
        let glyphs = layout.glyphRange(forCharacterRange: range, actualCharacterRange: nil)
        let rect = layout.boundingRect(forGlyphRange: glyphs, in: container)
        return window.convertToScreen(convert(rect, to: nil))
    }

    private func clearHover() {
        guard let range = hovered, let layout = layoutManager else { return }
        hovered = nil
        SpanTooltip.shared.hide()
        for key in [NSAttributedString.Key.backgroundColor, .foregroundColor, .underlineStyle, .underlineColor] {
            layout.removeTemporaryAttribute(key, forCharacterRange: range)
        }
        if let copied, copied.range == range {
            layout.addTemporaryAttribute(.backgroundColor, value: Self.copiedColor, forCharacterRange: range)
        }
    }

    private static let copiedColor = NSColor.systemGreen.withAlphaComponent(0.35)

    // MARK: Clicks

    /// A press that began on a span: its release is ours too, so it never reaches what is
    /// behind (a bubble row's button fires on release).
    private var pressedSpan = false

    override func mouseDown(with event: NSEvent) {
        guard let hit = span(at: point(of: event)) else {
            pressedSpan = false
            if selectableText { super.mouseDown(with: event) }
            return
        }
        pressedSpan = true
        RichTextClicks.spanPressed(at: event.timestamp)
        let outcome = actions.perform(spans[hit.index], reveal: event.modifierFlags.contains(.option))
        if outcome == .copied { flashCopied(hit.index, hit.range) }
    }

    override func mouseUp(with event: NSEvent) {
        if pressedSpan {
            pressedSpan = false
            return
        }
        super.mouseUp(with: event)
    }

    override func mouseDragged(with event: NSEvent) {
        if !pressedSpan { super.mouseDragged(with: event) }
    }

    /// The chip turns green and its tooltip says "Copied" for a moment.
    private func flashCopied(_ index: Int, _ range: NSRange) {
        guard let layoutManager else { return }
        let layout = layoutManager
        copiedReset?.cancel()
        if let old = copied { endCopied(old) }
        copied = (range, index)
        layout.addTemporaryAttribute(.backgroundColor, value: Self.copiedColor, forCharacterRange: range)
        if hovered == range { SpanTooltip.shared.show(spans[index].tooltip(copied: true), for: screenRect(of: range)) }
        let reset = DispatchWorkItem { [weak self] in
            guard let self, let c = self.copied else { return }
            self.copied = nil
            self.endCopied(c)
        }
        copiedReset = reset
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.5, execute: reset)
    }

    private func endCopied(_ c: (range: NSRange, index: Int)) {
        guard let layout = layoutManager, let storage = textStorage, NSMaxRange(c.range) <= storage.length,
              spans.indices.contains(c.index) else { return }
        layout.removeTemporaryAttribute(.backgroundColor, forCharacterRange: c.range)
        if hovered == c.range { hovered = nil; highlight(c.index, c.range) }
    }
}
