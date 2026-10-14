import AppKit
import DeskPixieCore
import os

/// Owns Bit on screen: the panel, the view, the player, the mouth level and the gestures.
///
/// Usage (coordinator, main actor):
/// ```swift
/// let settings = MascotSettings.fromSnapshot(try engine.call("settings.snapshot")) ?? .defaults
/// guard let mascot = MascotController.load(engine: engine, settings: settings) else { ... }
/// mascot.onDoubleClick = { coordinator.talk() }
/// mascot.show()
/// // .state → setState, .mouth → setMouth, .pack → applyPack, .position → moveTo,
/// // settings.changed → apply(settings:)
/// ```
/// Gestures (ai-mascot player.js): a single click calls `mascot.click` (agent terminal), a
/// double-click runs `onDoubleClick` (the voice hotkey), a drag moves the panel. A move by the
/// user (drag, or keep-feet on a size change) is reported with `mascot.moved {x,y}` once it has
/// settled for 400 ms; placements the core asked for (`position` event, launch) are not.
@MainActor
final class MascotController {
    static let cursorPollInterval: TimeInterval = 1.0 / 60.0
    /// A little past the 300 ms double-click window so timer jitter never expires it early.
    static let clickExpireDelay: TimeInterval = 0.31

    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "mascot")

    private let engine: CoreEngine
    private(set) var pack: Pack
    private(set) var sheet: SpriteSheet
    private var currentSheetPath: String?
    private(set) var scale: Int
    /// Bit's pixels in any frame (the `pack` event's mask, or built from the sheet at launch).
    private(set) var hitMask: HitMask
    private(set) var settings: MascotSettings
    private(set) var player: MascotPlayer
    private(set) var mouthLevel = 0
    /// Click / double-click / drag routing and the press state (DeskPixieCore, tested there).
    private let gestures: MascotGestureRouter
    /// The opacity last applied to the view (actions.rs `last_opacity`).
    private var lastOpacity: CGFloat = 1.0
    private var moves = MoveDebounce()
    /// Set while the controller places the panel itself, so the move is not reported as the user's.
    private var placing = false

    /// The voice hotkey action (double-click on Bit).
    var onDoubleClick: (() -> Void)? {
        get { gestures.onDoubleClick }
        set { gestures.onDoubleClick = newValue }
    }

    let panel: MascotPanel
    let view: MascotView

    private var animationTimer: Timer?
    private var cursorTimer: Timer?
    private var expireTimer: Timer?
    private var moveObserver: NSObjectProtocol?

    init(engine: CoreEngine, pack: Pack, sheet: SpriteSheet, sheetPath: String?, scale: Int, hitMask: HitMask?,
         settings: MascotSettings) {
        self.engine = engine
        currentSheetPath = sheetPath
        self.pack = pack
        self.sheet = sheet
        self.scale = scale
        self.hitMask = hitMask ?? HitMask.union(pack: pack, sheet: sheet)
        self.settings = settings
        player = MascotPlayer()
        gestures = MascotGestureRouter(engine: engine)
        let size = Layout.windowSize(pack: pack, scale: scale)
        let topLeft = Layout.restorePosition(settings.position, size: size, displays: ScreenDisplays())
        panel = MascotPanel(frame: ScreenDisplays.appKitFrame(topLeft: topLeft, size: size))
        view = MascotView(frame: NSRect(origin: .zero, size: size))
        panel.contentView = view
        view.onMouseDown = { [weak self] in self?.mouseDown($0) }
        view.onMouseDragged = { [weak self] in self?.mouseDragged($0) }
        view.onMouseUp = { [weak self] in self?.mouseUp($0) }
        moveObserver = NotificationCenter.default.addObserver(forName: NSWindow.didMoveNotification, object: panel,
                                                              queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.panelMoved() }
        }
        redraw()
    }

    /// Reads `engine.packJSON()` and loads the sheet; nil (logged) on failure. The scale is the
    /// payload's, else the saved one, else ×8; the hit mask is the payload's, else built here.
    static func load(engine: CoreEngine, settings: MascotSettings) -> MascotController? {
        guard let json = engine.packJSON(), let payload = PackPayload.decode(json) else {
            log.error("no usable pack from the core")
            return nil
        }
        guard let sheet = SpriteSheet(url: URL(fileURLWithPath: payload.sheetPath)) else {
            log.error("cannot load sheet \(payload.sheetPath, privacy: .public)")
            return nil
        }
        let scale = [payload.scale, settings.scale].compactMap { $0 }.first { $0 > 0 } ?? 8
        let mask = payload.hitMask.flatMap {
            HitMask(width: payload.pack.cellWidth, height: payload.pack.cellHeight, bits: $0)
        }
        log.info("ready: \(payload.pack.name, privacy: .public) \(payload.pack.cellWidth)x\(payload.pack.cellHeight) scale \(scale)")
        return MascotController(engine: engine, pack: payload.pack, sheet: sheet, sheetPath: payload.sheetPath, scale: scale,
                                hitMask: mask, settings: settings)
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
        if let moveObserver { NotificationCenter.default.removeObserver(moveObserver) }
        moveObserver = nil
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

    /// `EngineEvent.pack`: a new pack and/or scale. Reloads the sheet and the hit mask and
    /// resizes the panel keeping Bit's feet where they were (actions.rs `resize_window`); the
    /// moved top-left is then saved like a drag. A sheet that cannot be read keeps the old pack.
    func applyPack(_ ev: PackEvent) {
        let newSheet: SpriteSheet
        if ev.sheetPath == currentSheetPath {
            newSheet = sheet
        } else {
            guard let s = SpriteSheet(url: URL(fileURLWithPath: ev.sheetPath)) else {
                Self.log.error("cannot load sheet \(ev.sheetPath, privacy: .public)")
                return
            }
            newSheet = s
        }
        let fpsChanged = ev.pack.fps != pack.fps
        let oldSize = Layout.windowSize(pack: pack, scale: scale)
        pack = ev.pack
        sheet = newSheet
        currentSheetPath = ev.sheetPath
        scale = ev.scale > 0 ? ev.scale : scale
        hitMask = HitMask.forEvent(ev) ?? HitMask.union(pack: pack, sheet: sheet)
        player = MascotPlayer(state: pack.states[player.state] != nil ? player.state : "idle")
        mouthLevel = MascotPlayer.clampLevel(mouthLevel, pack: pack)
        let newSize = Layout.windowSize(pack: pack, scale: scale)
        if newSize != oldSize {
            let top = ScreenDisplays.desktopRect(panel.frame).origin
            let feet = Layout.keepFeet(top, old: oldSize, new: newSize)
            // A user-visible move: saved after it settles (Rust saves on every window move).
            panel.setFrame(ScreenDisplays.appKitFrame(topLeft: feet, size: newSize), display: false)
            view.frame = NSRect(origin: .zero, size: newSize)
        }
        if fpsChanged, animationTimer != nil {
            animationTimer?.invalidate()
            animationTimer = nil
            startTimers()
        }
        redraw()
        Self.log.info("pack: \(self.pack.name, privacy: .public) scale \(self.scale)")
    }

    /// `EngineEvent.position`: a desktop top-left (clamped onto a connected display like a
    /// restored position), or both nil for the default spot (Tray ▸ Reset position). Not
    /// reported back: the core already holds this position (or null).
    func moveTo(x: Double?, y: Double?) {
        let saved = x.flatMap { x in y.map { CGPoint(x: x, y: $0) } }
        let size = Layout.windowSize(pack: pack, scale: scale)
        let topLeft = Layout.restorePosition(saved, size: size, displays: ScreenDisplays())
        place(ScreenDisplays.appKitFrame(topLeft: topLeft, size: size))
    }

    /// New settings (`settings.changed`): fade on/off and radius take effect at the next poll.
    /// The position and scale are driven by `position` / `pack` events, not re-applied here.
    func apply(settings s: MascotSettings) {
        settings = s
    }

    /// Moves the panel without reporting it as the user's move.
    private func place(_ frame: NSRect) {
        placing = true
        panel.setFrame(frame, display: true)
        placing = false
        moves.cancel()
    }

    private func panelMoved() {
        guard !placing else { return }
        moves.moved(at: ProcessInfo.processInfo.systemUptime)
    }

    /// actions.rs `save_position_after_drag`: once the panel has stood still for 400 ms, its
    /// logical top-left goes to the core (`mascot.moved`), which saves it as `position`.
    private func reportMoveIfSettled() {
        guard moves.takeSettled(now: ProcessInfo.processInfo.systemUptime) else { return }
        let top = ScreenDisplays.desktopRect(panel.frame).origin
        let args = MovedArgs(x: top.x, y: top.y)
        let engine = engine
        Task.detached {
            do {
                _ = try engine.call("mascot.moved", args)
            } catch {
                Self.log.error("mascot.moved: \(String(describing: error), privacy: .public)")
            }
        }
    }

    // MARK: Timers

    private func startTimers() {
        guard animationTimer == nil else { return }
        animationTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / Double(max(pack.fps, 1)), repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.step() }
        }
        RunLoop.main.add(animationTimer!, forMode: .common)
        guard cursorTimer == nil else { return }
        // Cursor poll, as in oled-saver-macos CircleOverlayView (MIT): NSEvent global monitors only
        // fire for events going to *other* apps, so they stop while our app is frontmost;
        // NSEvent.mouseLocation works regardless of focus.
        cursorTimer = Timer.scheduledTimer(withTimeInterval: Self.cursorPollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.pollCursor() }
        }
        RunLoop.main.add(cursorTimer!, forMode: .common)
    }

    private func step() {
        player = player.stepped(pack: pack)
        redraw()
    }

    // MARK: Drawing

    private var viewHeight: CGFloat { CGFloat(pack.cellHeight * scale) }

    /// Converts a top-left-origin rect in cell pixels (moved down by `bob`) to view points.
    private func viewRect(x: CGFloat, y: CGFloat, w: CGFloat, h: CGFloat, bob: Int) -> CGRect {
        let s = CGFloat(scale)
        return CGRect(x: x * s, y: viewHeight - (y + CGFloat(bob) + h) * s, width: w * s, height: h * s)
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

    // MARK: Cursor poll: click-through, proximity fade, position save (actions.rs spawn_proximity)

    private func pollCursor() {
        reportMoveIfSettled()
        let top = ScreenDisplays.primaryMaxY
        // AppKit gives the cursor and the frame in the same global points on every display, so
        // tao's mixed-Retina correction (`rescale`) is the identity here.
        let cursor = DesktopSpace.desktopPoint(NSEvent.mouseLocation, primaryMaxY: top)
        let rect = DesktopSpace.desktopRect(panel.frame, primaryMaxY: top)
        // Click-through wherever no frame of Bit ever draws; never mid-press or mid-drag.
        let d = ClickThrough.decide(cursor: cursor, window: rect, mask: hitMask, pointerActive: gestures.pointerActive,
                                    leftButtonDown: NSEvent.pressedMouseButtons & 1 != 0,
                                    fadeEnabled: settings.fadeEnabled, fadeRadius: settings.fadeRadius)
        gestures.pointerActive = d.pointerActive
        if panel.ignoresMouseEvents != d.ignoresMouseEvents { panel.ignoresMouseEvents = d.ignoresMouseEvents }
        let next = d.opacity
        if ProximityFade.shouldEmit(last: lastOpacity, next: next) {
            view.setOpacity(Float(next))
            lastOpacity = next
        }
    }

    // MARK: Gestures

    private func mouseDown(_ e: NSEvent) {
        _ = gestures.down(at: e.locationInWindow, time: e.timestamp, leftButton: e.type == .leftMouseDown)
    }

    private func mouseDragged(_ e: NSEvent) {
        guard gestures.dragged(to: e.locationInWindow) == .beginDrag else { return }
        expireTimer?.invalidate()
        panel.performDrag(with: e)
    }

    /// Double-click talks (in the router); a single click waits out the double-click window.
    private func mouseUp(_ e: NSEvent) {
        switch gestures.up(at: e.locationInWindow, time: e.timestamp, leftButton: e.type == .leftMouseUp) {
        case .talked:
            expireTimer?.invalidate()
        case .scheduleClickExpiry:
            expireTimer?.invalidate()
            expireTimer = Timer.scheduledTimer(withTimeInterval: Self.clickExpireDelay, repeats: false) { [weak self] _ in
                MainActor.assumeIsolated { self?.expireClick() }
            }
        default:
            break
        }
    }

    /// A single click (no second press within 300 ms): `mascot.click` opens the agent terminal.
    private func expireClick() {
        _ = gestures.expire(now: ProcessInfo.processInfo.systemUptime)
    }
}
