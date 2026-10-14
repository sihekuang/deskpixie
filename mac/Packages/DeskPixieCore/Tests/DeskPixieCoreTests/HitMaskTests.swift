import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// The `pack` event's hit mask (P6 format: cell w×h, row-major, LSB-first bitset, base64) and
/// the click-through test built on it. Fixtures: `bit-sheet.png` (pixies/bit) and
/// `bit-hitmask.txt`, the Go core's `packs.HitMaskBase64` for that pack.
final class HitMaskTests: XCTestCase {
    private func fixture(_ name: String, _ ext: String) throws -> URL {
        try XCTUnwrap(Bundle.module.url(forResource: name, withExtension: ext, subdirectory: "Fixtures"))
    }

    private func bitPack() throws -> Pack {
        try XCTUnwrap(PackPayload.decode(Data(contentsOf: fixture("bit-pack", "json")))).pack
    }

    private func goMask() throws -> HitMask {
        let b64 = try String(contentsOf: fixture("bit-hitmask", "txt"), encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        let bits = try XCTUnwrap(Data(base64Encoded: b64))
        return try XCTUnwrap(HitMask(width: 32, height: 32, bits: bits))
    }

    func testBitsAreRowMajorLSBFirst() throws {
        // 3×3: (0,0) = bit 0 of byte 0; (2,0) = bit 2; (1,1) = index 4; (2,2) = index 8 = bit 0 of byte 1.
        let m = try XCTUnwrap(HitMask(width: 3, height: 3, bits: Data([0b0001_0101, 0b0000_0001])))
        let set = (0..<3).flatMap { y in (0..<3).compactMap { x in m.contains(x: x, y: y) ? "\(x),\(y)" : nil } }
        XCTAssertEqual(set, ["0,0", "2,0", "1,1", "2,2"])
        XCTAssertFalse(m.contains(x: -1, y: 0))
        XCTAssertFalse(m.contains(x: 3, y: 0))
        XCTAssertFalse(m.contains(x: 0, y: 3))
    }

    func testShortOrEmptyBitsAreRejected() {
        XCTAssertNil(HitMask(width: 3, height: 3, bits: Data([0xFF])), "9 bits need 2 bytes")
        XCTAssertNil(HitMask(width: 0, height: 3, bits: Data()))
        XCTAssertNotNil(HitMask(width: 8, height: 1, bits: Data([0])))
    }

    func testPackEventMaskDecodesForItsCell() throws {
        let pack = try bitPack()
        let ev = PackEvent(pack: pack, sheetPath: "/x", scale: 8, hitMask: try goMask().bits)
        let m = try XCTUnwrap(HitMask.forEvent(ev))
        XCTAssertEqual(m.width, 32)
        XCTAssertEqual(m.height, 32)
        XCTAssertNil(HitMask.forEvent(PackEvent(pack: pack, sheetPath: "/x", scale: 8, hitMask: nil)))
        XCTAssertNil(HitMask.forEvent(PackEvent(pack: pack, sheetPath: "/x", scale: 8, hitMask: Data([1, 2]))),
                     "a mask too short for the cell is ignored")
    }

    func testPackEventJSONCarriesTheMaskAsBase64() throws {
        var obj = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: fixture("bit-pack", "json"))) as? [String: Any])
        obj["kind"] = "pack"
        obj["scale"] = 6
        obj["hitMask"] = try String(contentsOf: fixture("bit-hitmask", "txt"), encoding: .utf8)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        guard case .pack(let ev)? = EngineEvent.decode(try JSONSerialization.data(withJSONObject: obj)) else {
            return XCTFail("not a pack event")
        }
        XCTAssertEqual(HitMask.forEvent(ev), try goMask())
    }

    /// The Swift fallback (launch, before any `pack` event) builds the same union as Go/Rust.
    func testSwiftUnionMatchesTheGoMask() throws {
        let sheet = try XCTUnwrap(SpriteSheet(url: fixture("bit-sheet", "png")))
        XCTAssertEqual(HitMask.union(pack: try bitPack(), sheet: sheet), try goMask())
    }

    /// Port of ai-mascot packs.rs `hit_at_maps_the_cursor_onto_the_mask`.
    func testHitAtMapsTheCursorOntoTheMask() throws {
        let m = try goMask()
        // 32×32 cells shown at ×8: 256×256 window at (1000, 500).
        let r = CGRect(x: 1000, y: 500, width: 256, height: 256)
        XCTAssertTrue(m.hit(cursor: CGPoint(x: 1000 + 16 * 8, y: 500 + 18 * 8), window: r), "body centre")
        XCTAssertFalse(m.hit(cursor: CGPoint(x: 1001, y: 501), window: r), "transparent corner")
        XCTAssertFalse(m.hit(cursor: CGPoint(x: 900, y: 600), window: r), "outside the window")
    }

    func testPackPayloadMayCarryScaleAndMask() throws {
        var obj = try XCTUnwrap(JSONSerialization.jsonObject(with: Data(contentsOf: fixture("bit-pack", "json"))) as? [String: Any])
        XCTAssertNil(PackPayload.decode(try JSONSerialization.data(withJSONObject: obj))?.hitMask)
        obj["hitMask"] = "AQID"
        obj["scale"] = 4
        let p = try XCTUnwrap(PackPayload.decode(try JSONSerialization.data(withJSONObject: obj)))
        XCTAssertEqual(p.hitMask, Data([1, 2, 3]))
        XCTAssertEqual(p.scale, 4)
    }
}
