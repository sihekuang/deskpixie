import CoreGraphics
import Foundation

public enum GestureResult: Equatable, Sendable {
    case none, press, drag, wait, double, click
}

/// Tells a click, a double-click and a drag on Pixie apart (port of player-core.js `gesture()`):
/// - press, then a move of more than `slop` points → `.drag` (the caller starts the window drag)
/// - press and release in place → `.wait`; if no second press comes within `double` seconds,
///   `expire(now:)` returns `.click`
/// - a second press within `double`, released in place → `.double` (the voice hotkey)
/// Times are seconds on one clock (`NSEvent.timestamp` / `ProcessInfo.systemUptime`).
/// Only the left button counts.
public struct Gesture: Sendable {
    private struct Press: Sendable {
        var at: CGPoint
        var second: Bool
    }

    /// Absorbs floating-point error so "exactly `double` apart" counts as inside the window,
    /// as with the JS integer milliseconds.
    private static let epsilon: TimeInterval = 1e-6

    private let slop: CGFloat
    private let double: TimeInterval
    private var press: Press?
    private var lastClickUp: TimeInterval?
    private var lastDoubleUp: TimeInterval?

    public init(slop: CGFloat = 3, double: TimeInterval = 0.3) {
        self.slop = slop
        self.double = double
    }

    /// True while a left press is held (before it turned into a drag or was released).
    public var isPressed: Bool { press != nil }

    private func within(_ a: TimeInterval?, _ b: TimeInterval) -> Bool {
        guard let a else { return false }
        return b - a <= double + Self.epsilon
    }

    private func moved(_ p: CGPoint, from q: CGPoint) -> Bool {
        abs(p.x - q.x) > slop || abs(p.y - q.y) > slop
    }

    public mutating func down(at p: CGPoint, time: TimeInterval, leftButton: Bool) -> GestureResult {
        guard leftButton else {
            press = nil
            return .none
        }
        if within(lastDoubleUp, time) {
            // Third (or later) rapid click of a multi-click: swallowed, so it never becomes a click.
            press = nil
            return .none
        }
        press = Press(at: p, second: within(lastClickUp, time))
        return .press
    }

    public mutating func move(to p: CGPoint) -> GestureResult {
        guard let pr = press, moved(p, from: pr.at) else { return .none }
        press = nil
        // Deliberate: a click followed by a drag drops the first click.
        lastClickUp = nil
        return .drag
    }

    public mutating func up(at p: CGPoint, time: TimeInterval, leftButton: Bool) -> GestureResult {
        let pr = press
        press = nil
        guard let pr, leftButton else { return .none }
        if moved(p, from: pr.at) {
            lastClickUp = nil
            return .none
        }
        if pr.second {
            lastClickUp = nil
            lastDoubleUp = time
            return .double
        }
        lastClickUp = time
        return .wait
    }

    public mutating func expire(now: TimeInterval) -> GestureResult {
        if let pr = press, pr.second { return .none }
        guard lastClickUp != nil, !within(lastClickUp, now) else { return .none }
        lastClickUp = nil
        return .click
    }
}
