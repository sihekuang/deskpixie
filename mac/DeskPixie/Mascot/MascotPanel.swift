import AppKit

/// Borderless, non-activating, transparent floating panel that hosts Bit on every Space.
final class MascotPanel: NSPanel {
    static let screenMargin: CGFloat = 24

    /// Creates the panel at the bottom-right of the main screen's visible frame (24 pt margin).
    init(contentSize: NSSize) {
        let visible = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let origin = NSPoint(x: visible.maxX - contentSize.width - Self.screenMargin,
                             y: visible.minY + Self.screenMargin)
        super.init(contentRect: NSRect(origin: origin, size: contentSize),
                   styleMask: [.borderless, .nonactivatingPanel],
                   backing: .buffered, defer: false)
        isOpaque = false
        backgroundColor = .clear
        hasShadow = false
        level = .floating
        collectionBehavior = [.canJoinAllSpaces, .stationary, .fullScreenAuxiliary]
        isReleasedWhenClosed = false
        acceptsMouseMovedEvents = true
        becomesKeyOnlyIfNeeded = true
        hidesOnDeactivate = false
    }

    override var canBecomeKey: Bool { false }
    override var canBecomeMain: Bool { false }
}
