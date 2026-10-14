import CoreGraphics
import Foundation

/// Which pixels of a cell belong to Bit in *any* frame (ai-mascot `mascot-pack` hit_mask.rs):
/// the union of every body frame, the mouth at every level and face offset, and the body
/// smeared down by `bob.px`. The cursor poll makes the panel clickable only over these pixels.
///
/// Wire format (the `pack` event's `hitMask`, Go `packs.HitMaskBase64`): `width × height`
/// = the pack's cell, row-major, LSB-first: pixel (x, y) is bit `(y*w+x) % 8` of byte
/// `(y*w+x) / 8`.
public struct HitMask: Equatable, Sendable {
    public let width: Int
    public let height: Int
    public let bits: Data

    /// nil when the size is empty or `bits` is too short for it.
    public init?(width: Int, height: Int, bits: Data) {
        guard width > 0, height > 0, bits.count >= (width * height + 7) / 8 else { return nil }
        self.width = width
        self.height = height
        self.bits = Data(bits) // re-base so indices start at 0
    }

    /// The mask a `pack` event carries for its pack's cell; nil when absent or malformed.
    public static func forEvent(_ ev: PackEvent) -> HitMask? {
        ev.hitMask.flatMap { HitMask(width: ev.pack.cellWidth, height: ev.pack.cellHeight, bits: $0) }
    }

    public func contains(x: Int, y: Int) -> Bool {
        guard x >= 0, y >= 0, x < width, y < height else { return false }
        let i = y * width + x
        return bits[i / 8] & (1 << UInt8(i % 8)) != 0
    }

    /// packs.rs `hit_at`: whether the cursor is over one of Bit's pixels. `window` is the
    /// pixie window, which shows one cell scaled to fill it (desktop coordinates, y down).
    public func hit(cursor: CGPoint, window: CGRect) -> Bool {
        ProximityFade.cursorCell(cursor, rect: window, cell: (width, height)).map { contains(x: $0.x, y: $0.y) } ?? false
    }

    /// Builds the mask from the sheet, as the Go core does (`packs.buildMask`). Used at launch,
    /// before the core has sent a `pack` event with its own mask.
    public static func union(pack: Pack, sheet: SpriteSheet) -> HitMask {
        let w = max(pack.cellWidth, 1), h = max(pack.cellHeight, 1)
        var grid = [Bool](repeating: false, count: w * h)
        func set(_ x: Int, _ y: Int) {
            if x >= 0, y >= 0, x < w, y < h { grid[y * w + x] = true }
        }
        /// Copies the opaque pixels of the `size` region at `src` (sheet pixels) to `at`.
        func stamp(src: (Int, Int), size: (Int, Int), at: (Int, Int)) {
            for y in 0..<max(min(size.1, h), 0) {
                for x in 0..<max(min(size.0, w), 0) where sheet.isOpaque(sheetPoint: CGPoint(x: src.0 + x, y: src.1 + y)) {
                    set(at.0 + x, at.1 + y)
                }
            }
        }
        for s in pack.states.values {
            for f in 0..<max(s.frames, 0) {
                stamp(src: (f * w, s.row * h), size: (w, h), at: (0, 0))
            }
        }
        let m = pack.mouth
        let dys = Set([0] + pack.states.values.flatMap(\.faceDy))
        for level in 0..<max(m.levels, 0) {
            for dy in dys {
                stamp(src: (level * w, m.row * h), size: (m.size[0], m.size[1]), at: (m.anchor[0], m.anchor[1] + dy))
            }
        }
        let base = grid
        let bob = min(max(pack.bob.px, 0), h)
        for y in 0..<h {
            for x in 0..<w where base[y * w + x] {
                for d in stride(from: 1, through: bob, by: 1) { set(x, y + d) }
            }
        }
        var bytes = Data(count: (w * h + 7) / 8)
        for (i, on) in grid.enumerated() where on {
            bytes[i / 8] |= 1 << UInt8(i % 8)
        }
        return HitMask(width: w, height: h, bits: bytes)!
    }
}
