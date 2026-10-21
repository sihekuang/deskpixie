import AppKit
import QuartzCore

/// Layer-backed view drawing Pixie as two nearest-neighbour-scaled layers: body and mouth.
/// Mouse events are forwarded to the controller (window coordinates, `event.timestamp`).
final class PixieView: NSView {
    private let bodyLayer = CALayer()
    private let mouthLayer = CALayer()

    var onMouseDown: ((NSEvent) -> Void)?
    var onMouseDragged: ((NSEvent) -> Void)?
    var onMouseUp: ((NSEvent) -> Void)?
    /// A secondary click (right-click, or control-click).
    var onSecondaryClick: ((NSEvent) -> Void)?
    /// Files or an image dropped on Pixie (#240): true when taken.
    var onDrop: ((NSPasteboard) -> Bool)?

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        let root = CALayer()
        root.backgroundColor = NSColor.clear.cgColor
        root.masksToBounds = true // a bobbing body clips its bottom row, as the JS canvas did
        layer = root
        for l in [bodyLayer, mouthLayer] {
            l.magnificationFilter = .nearest
            l.minificationFilter = .nearest
            l.contentsGravity = .resize
            l.actions = ["contents": NSNull(), "position": NSNull(), "bounds": NSNull(), "frame": NSNull()]
            root.addSublayer(l)
        }
        registerForDraggedTypes([.fileURL, .png, .tiff])
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) is not used") }

    override var isOpaque: Bool { false }
    override var mouseDownCanMoveWindow: Bool { false }
    override func acceptsFirstMouse(for event: NSEvent?) -> Bool { true }

    /// Frames are in view points with a bottom-left origin (the controller does the y flip).
    func render(body: CGImage?, bodyFrame: CGRect, mouth: CGImage?, mouthFrame: CGRect) {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        bodyLayer.contents = body
        bodyLayer.frame = bodyFrame
        mouthLayer.contents = mouth
        mouthLayer.frame = mouthFrame
        CATransaction.commit()
    }

    func setOpacity(_ value: Float) {
        guard let layer, layer.opacity != value else { return }
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        layer.opacity = value
        CATransaction.commit()
    }

    override func mouseDown(with event: NSEvent) {
        if event.modifierFlags.contains(.control) { onSecondaryClick?(event) } else { onMouseDown?(event) }
    }
    override func rightMouseDown(with event: NSEvent) { onSecondaryClick?(event) }

    override func draggingEntered(_ sender: NSDraggingInfo) -> NSDragOperation { onDrop == nil ? [] : .copy }
    override func performDragOperation(_ sender: NSDraggingInfo) -> Bool { onDrop?(sender.draggingPasteboard) ?? false }
    override func mouseDragged(with event: NSEvent) { onMouseDragged?(event) }
    override func mouseUp(with event: NSEvent) {
        if !event.modifierFlags.contains(.control) { onMouseUp?(event) }
    }
}
