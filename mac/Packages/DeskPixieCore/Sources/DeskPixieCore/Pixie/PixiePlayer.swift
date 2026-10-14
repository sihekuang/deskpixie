import CoreGraphics

/// Pure pixie playback logic: a port of ai-mascot ui/mascot/player-core.js.
/// All rects and points are in sheet (or cell) pixels with a top-left origin.
public struct PixiePlayer: Equatable, Sendable {
    public var state: String
    public var frame: Int
    public var tick: Int

    public init(state: String = "idle") {
        self.state = state
        frame = 0
        tick = 0
    }

    /// setState: an unknown state or the current state leaves the player unchanged;
    /// otherwise switches immediately, starting at frame 0 (tick keeps counting).
    public func setting(_ name: String, pack: Pack) -> PixiePlayer {
        guard pack.states[name] != nil, name != state else { return self }
        var p = self
        p.state = name
        p.frame = 0
        return p
    }

    /// step: next frame; a looping state wraps, a non-looping one ends in idle.
    public func stepped(pack: Pack) -> PixiePlayer {
        var p = self
        p.tick = tick + 1
        guard let spec = pack.states[state] else {
            p.state = "idle"
            p.frame = 0
            return p
        }
        let next = frame + 1
        if next < spec.frames {
            p.frame = next
        } else if spec.loop {
            p.frame = 0
        } else {
            p.state = "idle"
            p.frame = 0
        }
        return p
    }

    /// Body offset down, in cell pixels: alternates between 0 and bob.px every bob.frames ticks.
    public func bobOffset(pack: Pack) -> Int {
        guard let spec = pack.states[state], spec.bob, pack.bob.px != 0, pack.bob.frames > 0 else { return 0 }
        return (tick / pack.bob.frames) % 2 == 1 ? pack.bob.px : 0
    }

    /// The current body cell in the sheet (column = frame, row = state's row).
    public func bodyRect(pack: Pack) -> CGRect {
        let w = pack.cellWidth, h = pack.cellHeight
        let row = pack.states[state]?.row ?? 0
        return CGRect(x: frame * w, y: row * h, width: w, height: h)
    }

    public static func clampLevel(_ level: Int, pack: Pack) -> Int {
        min(max(level, 0), max(pack.mouth.levels - 1, 0))
    }

    /// The mouth sprite for `level` (column = clamped level, row = mouth row).
    public static func mouthRect(level: Int, pack: Pack) -> CGRect {
        CGRect(x: clampLevel(level, pack: pack) * pack.cellWidth, y: pack.mouth.row * pack.cellHeight,
               width: pack.mouth.size[0], height: pack.mouth.size[1])
    }

    /// Where the mouth's top-left goes inside the cell: the anchor moved by this frame's faceDy.
    public func mouthPos(pack: Pack) -> CGPoint {
        let dy = pack.states[state].flatMap { $0.faceDy.indices.contains(frame) ? $0.faceDy[frame] : nil } ?? 0
        return CGPoint(x: pack.mouth.anchor[0], y: pack.mouth.anchor[1] + dy)
    }
}

/// Orders pack loads that wait on an image decode (port of player-core.js `loadSequencer`):
/// - `begin(state:)` starts a load and returns its token
/// - `noteState(_:)` records a state event; one arriving during a decode wins over the
///   (older) payload state
/// - `finish(_:)` returns the state to apply, or nil when a newer load has started since,
///   so loads finishing out of order never apply a stale pack
public struct LoadSequencer: Sendable {
    private var seq = 0
    private var wanted: String?

    public init() {}

    public mutating func begin(state: String) -> Int {
        seq += 1
        wanted = state
        return seq
    }

    public mutating func noteState(_ name: String) {
        wanted = name
    }

    public func finish(_ token: Int) -> String? {
        token == seq ? wanted : nil
    }
}
