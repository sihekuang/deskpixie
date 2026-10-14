import CoreGraphics
import Foundation
import os

/// What the mascot panel does after a mouse event on Bit.
public enum MascotGestureAction: Equatable, Sendable {
    case none
    /// The press moved past the slop: start the window drag (`performDrag`).
    case beginDrag
    /// A click was released: call `expire(now:)` once the double-click window has passed.
    case scheduleClickExpiry
    /// Double-click: the voice hotkey ran (`onDoubleClick`, else `engine.hotkey()`).
    case talked
    /// Single click: `mascot.click` went to the core (it opens the agent terminal).
    case openedTerminal
}

/// Routes mouse events on Bit (player.js gestures, V-043 / D-449): a single click calls
/// `mascot.click`, a double-click talks, a drag moves the panel. Tracks whether a press or drag
/// is active for the click-through decision.
@MainActor
public final class MascotGestureRouter {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "mascot")

    private let engine: CoreEngine
    private var gesture = Gesture()
    /// True from a left press until the button is up (covers the window drag too).
    public var pointerActive = false
    /// The voice hotkey action; nil sends `engine.hotkey()`.
    public var onDoubleClick: (() -> Void)?

    public init(engine: CoreEngine) {
        self.engine = engine
    }

    public func down(at p: CGPoint, time: TimeInterval, leftButton: Bool) -> MascotGestureAction {
        if gesture.down(at: p, time: time, leftButton: leftButton) == .press { pointerActive = true }
        return .none
    }

    public func dragged(to p: CGPoint) -> MascotGestureAction {
        guard gesture.move(to: p) == .drag else { return .none }
        pointerActive = true
        return .beginDrag
    }

    public func up(at p: CGPoint, time: TimeInterval, leftButton: Bool) -> MascotGestureAction {
        let r = gesture.up(at: p, time: time, leftButton: leftButton)
        pointerActive = false
        switch r {
        case .double:
            Self.log.info("double-click: voice hotkey")
            if let onDoubleClick { onDoubleClick() } else { engine.hotkey() }
            return .talked
        case .wait:
            return .scheduleClickExpiry
        default:
            return .none
        }
    }

    /// A single click (no second press within 300 ms): the core opens the agent terminal.
    public func expire(now: TimeInterval) -> MascotGestureAction {
        guard gesture.expire(now: now) == .click else { return .none }
        Self.log.info("click on mascot")
        let engine = engine
        Task.detached {
            do {
                _ = try engine.call("mascot.click")
            } catch {
                Self.log.error("mascot.click: \(String(describing: error), privacy: .public)")
            }
        }
        return .openedTerminal
    }
}
