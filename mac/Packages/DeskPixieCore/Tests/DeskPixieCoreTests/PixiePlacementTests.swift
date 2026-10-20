import CoreGraphics
import XCTest
@testable import DeskPixieCore

/// actions.rs `save_position_after_drag` (400 ms settle) and the pixie's view of
/// `settings.snapshot` values (position, scale, proximityFade).
final class PixiePlacementTests: XCTestCase {
    func testAMoveIsReportedOnceAfter400msOfStillness() {
        var d = MoveDebounce()
        XCTAssertFalse(d.takeSettled(now: 10), "nothing moved")
        d.moved(at: 10)
        XCTAssertFalse(d.takeSettled(now: 10.399))
        d.moved(at: 10.25) // still dragging: the clock restarts
        XCTAssertFalse(d.takeSettled(now: 10.6))
        XCTAssertTrue(d.takeSettled(now: 10.7))
        XCTAssertFalse(d.takeSettled(now: 11), "reported once")
        XCTAssertEqual(MoveDebounce.settle, 0.4)
    }

    func testCancelDropsAPendingMove() {
        var d = MoveDebounce()
        d.moved(at: 1)
        d.cancel()
        XCTAssertFalse(d.takeSettled(now: 5))
    }

    private func snapshot(_ values: String) -> Data {
        Data(#"{"platform":"macos","values":\#(values),"fields":[],"restartPending":[],"seq":3}"#.utf8)
    }

    func testSettingsComeFromTheSnapshotValues() throws {
        let s = try XCTUnwrap(PixieSettings.fromSnapshot(snapshot(
            #"{"scale":6,"position":[-900.5,300],"proximityFade":{"enabled":false,"radius":200},"pack":"bit"}"#)))
        XCTAssertEqual(s.position, CGPoint(x: -900.5, y: 300))
        XCTAssertEqual(s.scale, 6)
        XCTAssertFalse(s.fadeEnabled)
        XCTAssertEqual(s.fadeRadius, 200)
    }

    func testMissingOrNullValuesFallBackToTheDefaults() throws {
        let s = try XCTUnwrap(PixieSettings.fromSnapshot(snapshot(#"{"position":null}"#)))
        XCTAssertNil(s.position)
        XCTAssertNil(s.scale)
        XCTAssertFalse(s.fadeEnabled)
        XCTAssertEqual(s.fadeRadius, 150)
        XCTAssertEqual(PixieSettings.defaults, PixieSettings(position: nil, scale: nil, fadeEnabled: false, fadeRadius: 150))
        XCTAssertNil(PixieSettings.fromSnapshot(Data("nope".utf8)))
        let bad = try XCTUnwrap(PixieSettings.fromSnapshot(snapshot(#"{"position":[1],"scale":0}"#)))
        XCTAssertNil(bad.position, "a position needs two numbers")
        XCTAssertNil(bad.scale, "scale 0 means unset (config.rs normalizes it to 8)")
    }

    func testMovedArgsEncodeAsXY() throws {
        let json = String(decoding: try JSONEncoder().encode(MovedArgs(x: 12.5, y: -3)), as: UTF8.self)
        XCTAssertTrue(json.contains(#""x":12.5"#), json)
        XCTAssertTrue(json.contains(#""y":-3"#), json)
    }

    func testWindowSizeIsCellTimesScale() throws {
        let pack = try XCTUnwrap(PackPayload.decode(Data(contentsOf: XCTUnwrap(
            Bundle.module.url(forResource: "bit-pack", withExtension: "json", subdirectory: "Fixtures"))))).pack
        XCTAssertEqual(Layout.windowSize(pack: pack, scale: 8), CGSize(width: 256, height: 256))
        XCTAssertEqual(Layout.windowSize(pack: pack, scale: 0), CGSize(width: 256, height: 256), "0 = default ×8")
    }
}
