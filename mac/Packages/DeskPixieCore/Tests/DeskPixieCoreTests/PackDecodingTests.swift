import Foundation
import XCTest
@testable import DeskPixieCore

final class PackDecodingTests: XCTestCase {
    func testDecodesTheResolvedBitPackPayload() throws {
        let url = try XCTUnwrap(Bundle.module.url(forResource: "bit-pack", withExtension: "json", subdirectory: "Fixtures"))
        let payload = try XCTUnwrap(PackPayload.decode(try Data(contentsOf: url)))
        XCTAssertEqual(payload.sheetPath, "/abs/pixies/bit/sheet.png")
        let p = payload.pack
        XCTAssertEqual(p.name, "Bit")
        XCTAssertEqual(p.cell, [32, 32])
        XCTAssertEqual(p.fps, 8)
        XCTAssertEqual(p.bob, Pack.Bob(px: 1, frames: 4))
        XCTAssertEqual(Set(p.states.keys), ["idle", "listening", "thinking", "delegating", "happy", "error"])
        XCTAssertEqual(p.states["happy"], Pack.StateSpec(row: 4, frames: 8, loop: false, bob: false,
                                                          faceDy: [0, -1, -2, -2, -2, -1, 0, 0]))
        XCTAssertEqual(p.states["error"], p.states["idle"])
        XCTAssertEqual(p.mouth, Pack.Mouth(row: 5, anchor: [12, 20], size: [8, 5], levels: 4))
    }

    func testUnresolvedFieldsGetTheResolverDefaults() throws {
        let json = #"{"name":"X","cell":[16,16],"fps":4,"states":{"idle":{"row":0,"frames":2}},"mouth":{"row":1,"anchor":[1,2],"size":[3,4],"levels":2}}"#
        let p = try JSONDecoder().decode(Pack.self, from: Data(json.utf8))
        XCTAssertEqual(p.bob, Pack.Bob(px: 0, frames: 1))
        XCTAssertEqual(p.states["idle"], Pack.StateSpec(row: 0, frames: 2, loop: true, bob: true, faceDy: [0, 0]))
    }

    func testMalformedPayloadIsNil() {
        XCTAssertNil(PackPayload.decode(Data("{}".utf8)))
        XCTAssertNil(PackPayload.decode(Data("nope".utf8)))
    }
}
