import AppKit
import DeskPixieCore
import os

/// Owns Bit on screen: the panel, the view, the player, the mouth level and the gestures.
///
/// Usage (coordinator, main actor):
/// ```swift
/// guard let mascot = MascotController.load(engine: engine) else { ... }   // after engine.configure
/// mascot.show()
/// // EngineEvent.state(s) → mascot.setState(s); EngineEvent.mouth(l) → mascot.setMouth(l)
/// ```
/// A double-click on Bit calls `engine.hotkey()` (the voice toggle); a single click is
/// logged only (the agent terminal is Phase 5); a drag moves the panel.
@MainActor
final class MascotController {
    static let defaultScale: CGFloat = 8
    static let fadeRadius: CGFloat = 150
    static let cursorPollInterval: TimeInterval = 1.0 / 60.0
    /// A little past the 300 ms double-click window so timer jitter never expires it early.
    static let clickExpireDelay: TimeInterval = 0.31

    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "mascot")

    private let engine: CoreEngine
    let pack: Pack
    let sheet: SpriteSheet
    let scale: CGFloat
    private(set) var player: MascotPlayer
    private(set) var mouthLevel = 0
    private var gesture = Gesture()
    /// True from a left press until all buttons are up (covers the window drag too).
    private var pointerActive = false

    let panel: MascotPanel
    let view: MascotView

    private var animationTimer: Timer?
    private var cursorTimer: Timer?
    private var expireTimer: Timer?

    init(engine: CoreEngine, pack: Pack, sheet: SpriteSheet, scale: CGFloat = MascotController.defaultScale) {
        self.engine = engine
        self.pack = pack
        self.sheet = sheet
        self.scale = scale
        player = MascotPlayer()
        let size = NSSize(width: CGFloat(pack.cellWidth) * scale, height: CGFloat(pack.cellHeight) * scale)
        panel = MascotPanel(contentSize: size)
        view = MascotView(frame: NSRect(origin: .zero, size: size))
        panel.contentView = view
        view.onMouseDown = { [weak self] in self?.mouseDown($0) }
        view.onMouseDragged = { [weak self] in self?.mouseDragged($0) }
        view.onMouseUp = { [weak self] in self?.mouseUp($0) }
        redraw()
    }

    /// Reads `engine.packJSON()` (`{"pack":…, "sheetPath":…}`) and loads the sheet; nil (logged) on failure.
    static func load(engine: CoreEngine, scale: CGFloat = defaultScale) -> MascotController? {
        guard let json = engine.packJSON(), let payload = PackPayload.decode(json) else {
            log.error("no usable pack from the core")
            return nil
        }
        guard let sheet = SpriteSheet(url: URL(fileURLWithPath: payload.sheetPath)) else {
            log.error("cannot load sheet \(payload.sheetPath, privacy: .public)")
            return nil
        }
        log.info("ready: \(payload.pack.name, privacy: .public) \(payload.pack.cellWidth)x\(payload.pack.cellHeight) scale \(scale)")
        return MascotController(engine: engine, pack: payload.pack, sheet: sheet, scale: scale)
    }

    /// Shows the panel and starts the animation timer and the 60 Hz cursor poll.
    func show() {
        panel.orderFrontRegardless()
        startTimers()
    }

    /// Hides the panel and stops all timers.
    func stop() {
        animationTimer?.invalidate()
        cursorTimer?.invalidate()
        expireTimer?.invalidate()
        animationTimer = nil
        cursorTimer = nil
        expireTimer = nil
        panel.orderOut(nil)
    }

    // MARK: Engine events

    /// `EngineEvent.state`: unknown states and the current state are ignored.
    func setState(_ name: String) {
        let next = player.setting(name, pack: pack)
        if next == player, pack.states[name] == nil {
            Self.log.warning("unknown mascot state \(name, privacy: .public)")
        }
        player = next
        redraw()
    }

    /// `EngineEvent.mouth`: level 0…levels-1 (clamped).
    func setMouth(_ level: Int) {
        let l = MascotPlayer.clampLevel(level, pack: pack)
        guard l != mouthLevel else { return }
        mouthLevel = l
        redraw()
    }

    // MARK: Timers

    private func startTimers() {
        guard animationTimer == nil else { return }
        animationTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / Double(max(pack.fps, 1)), repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.step() }
        }
        // Cursor poll, as in oled-saver-macos CircleOverlayView (MIT): NSEvent global monitors only
        // fire for events going to *other* apps, so they stop while our app is frontmost;
        // NSEvent.mouseLocation works regardless of focus.
        cursorTimer = Timer.scheduledTimer(withTimeInterval: Self.cursorPollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollCursor() }
        }
        for t in [animationTimer, cursorTimer] { t.map { RunLoop.main.add($0, forMode: .common) } }
    }

    private func step() {
        player = player.stepped(pack: pack)
        redraw()
    }

    // MARK: Drawing

    private var viewHeight: CGFloat { CGFloat(pack.cellHeight) * scale }

    /// Converts a top-left-origin rect in cell pixels (moved down by `bob`) to view points.
    private func viewRect(x: CGFloat, y: CGFloat, w: CGFloat, h: CGFloat, bob: Int) -> CGRect {
        CGRect(x: x * scale, y: viewHeight - (y + CGFloat(bob) + h) * scale, width: w * scale, height: h * scale)
    }

    private func redraw() {
        let bob = player.bobOffset(pack: pack)
        let body = player.bodyRect(pack: pack)
        let mouth = MascotPlayer.mouthRect(level: mouthLevel, pack: pack)
        let at = player.mouthPos(pack: pack)
        view.render(body: sheet.crop(body),
                    bodyFrame: viewRect(x: 0, y: 0, w: body.width, h: body.height, bob: bob),
                    mouth: sheet.crop(mouth),
                    mouthFrame: viewRect(x: at.x, y: at.y, w: mouth.width, h: mouth.height, bob: bob))
    }

    // MARK: Cursor poll: click-through and proximity fade

    /// The sheet pixel under a screen point, inside the current body cell; nil outside it.
    private func bodySheetPoint(screen p: NSPoint) -> CGPoint? {
        let f = panel.frame
        guard f.contains(p) else { return nil }
        let cellX = (p.x - f.minX) / scale
        let cellY = (f.maxY - p.y) / scale - CGFloat(player.bobOffset(pack: pack)) // top-left origin
        guard cellX >= 0, cellY >= 0, cellX < CGFloat(pack.cellWidth), cellY < CGFloat(pack.cellHeight) else { return nil }
        let body = player.bodyRect(pack: pack)
        return CGPoint(x: body.minX + cellX, y: body.minY + cellY)
    }

    private func pollCursor() {
        let cursor = NSEvent.mouseLocation
        if pointerActive, NSEvent.pressedMouseButtons & 1 == 0 { pointerActive = false }
        // Click-through wherever Bit is transparent; never mid-press or mid-drag.
        let overBit = bodySheetPoint(screen: cursor).map { sheet.isOpaque(sheetPoint: $0) } ?? false
        let ignore = !(pointerActive || overBit)
        if panel.ignoresMouseEvents != ignore { panel.ignoresMouseEvents = ignore }
        let opacity = ProximityFade.opacity(spriteFrame: panel.frame, cursor: cursor,
                                            fadeRadius: Self.fadeRadius, enabled: true)
        view.setOpacity(Float(opacity))
    }

    // MARK: Gestures

    private func mouseDown(_ e: NSEvent) {
        if gesture.down(at: e.locationInWindow, time: e.timestamp, leftButton: e.type == .leftMouseDown) == .press {
            pointerActive = true
        }
    }

    private func mouseDragged(_ e: NSEvent) {
        guard gesture.move(to: e.locationInWindow) == .drag else { return }
        expireTimer?.invalidate()
        pointerActive = true
        panel.performDrag(with: e)
    }

    private func mouseUp(_ e: NSEvent) {
        let r = gesture.up(at: e.locationInWindow, time: e.timestamp, leftButton: e.type == .leftMouseUp)
        pointerActive = false
        switch r {
        case .double:
            expireTimer?.invalidate()
            Self.log.info("double-click: voice hotkey")
            engine.hotkey()
        case .wait:
            expireTimer?.invalidate()
            expireTimer = Timer.scheduledTimer(withTimeInterval: Self.clickExpireDelay, repeats: false) { [weak self] _ in
                MainActor.assumeIsolated { self?.expireClick() }
            }
        default:
            break
        }
    }

    private func expireClick() {
        if gesture.expire(now: ProcessInfo.processInfo.systemUptime) == .click {
            // The agent terminal opens here in Phase 5.
            Self.log.info("click on mascot (no action in Phase 3)")
        }
    }
}
