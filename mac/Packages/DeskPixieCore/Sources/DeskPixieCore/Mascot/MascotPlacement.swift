import CoreGraphics
import Foundation

/// actions.rs `save_position_after_drag`: a window move is reported once it has stood still
/// for 400 ms, so a drag saves one position, not hundreds. Times are seconds on one clock.
public struct MoveDebounce: Sendable {
    public static let settle: TimeInterval = 0.4

    private var movedAt: TimeInterval?

    public init() {}

    /// The window moved (each step of a drag restarts the clock).
    public mutating func moved(at t: TimeInterval) { movedAt = t }

    /// Forgets a pending move (e.g. the window was placed by the core, not the user).
    public mutating func cancel() { movedAt = nil }

    /// True once per move, when it has settled; the caller then reports the position.
    public mutating func takeSettled(now: TimeInterval) -> Bool {
        guard let t = movedAt, now - t >= Self.settle else { return false }
        movedAt = nil
        return true
    }
}

/// `mascot.moved` arguments: the window's logical top-left in desktop coordinates
/// (`DesktopSpace`), which the core saves as settings `position`.
public struct MovedArgs: Encodable, Sendable, Equatable {
    public let x: Double
    public let y: Double

    public init(x: Double, y: Double) {
        self.x = x
        self.y = y
    }
}

/// The settings the mascot window follows, read from `settings.snapshot` `values`
/// (settings.json: `position`, `scale`, `proximityFade.{enabled,radius}`).
public struct MascotSettings: Equatable, Sendable {
    /// Saved top-left in desktop coordinates; nil = the default spot.
    public var position: CGPoint?
    /// nil when unset (the pack event or the default ×8 decides).
    public var scale: Int?
    public var fadeEnabled: Bool
    /// In points (Rust multiplies by the display scale to get physical pixels; AppKit points
    /// already are logical).
    public var fadeRadius: Double

    public init(position: CGPoint?, scale: Int?, fadeEnabled: Bool, fadeRadius: Double) {
        self.position = position
        self.scale = scale
        self.fadeEnabled = fadeEnabled
        self.fadeRadius = fadeRadius
    }

    /// config.rs defaults: no position, fade on, radius 150.
    public static let defaults = MascotSettings(position: nil, scale: nil, fadeEnabled: true, fadeRadius: 150)

    private struct Snapshot: Decodable {
        struct Fade: Decodable {
            var enabled: Bool?
            var radius: Double?
        }

        struct Values: Decodable {
            var position: [Double]?
            var scale: Int?
            var proximityFade: Fade?

            private enum CodingKeys: String, CodingKey { case position, scale, proximityFade }

            /// Lenient: a malformed field falls back to its default instead of failing the rest.
            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                position = try? c.decodeIfPresent([Double].self, forKey: .position)
                scale = try? c.decodeIfPresent(Int.self, forKey: .scale)
                proximityFade = try? c.decodeIfPresent(Fade.self, forKey: .proximityFade)
            }
        }

        var values: Values
    }

    /// nil when the JSON is not a snapshot.
    public static func fromSnapshot(_ json: Data) -> MascotSettings? {
        guard let v = try? JSONDecoder().decode(Snapshot.self, from: json).values else { return nil }
        var s = defaults
        if let p = v.position, p.count == 2 { s.position = CGPoint(x: p[0], y: p[1]) }
        if let n = v.scale, n > 0 { s.scale = n }
        if let e = v.proximityFade?.enabled { s.fadeEnabled = e }
        if let r = v.proximityFade?.radius { s.fadeRadius = r }
        return s
    }
}
