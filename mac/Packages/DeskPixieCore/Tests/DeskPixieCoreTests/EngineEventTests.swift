import XCTest
@testable import DeskPixieCore

final class EngineEventTests: XCTestCase {
    private func d(_ s: String) -> EngineEvent? { EngineEvent.decode(Data(s.utf8)) }

    func testDecodeKinds() {
        XCTAssertEqual(d(#"{"kind":"state","state":"talking"}"#), .state("talking"))
        XCTAssertEqual(d(#"{"kind":"mouth","level":3}"#), .mouth(3))
        XCTAssertEqual(d(#"{"kind":"status","text":"OPENAI_API_KEY not set"}"#), .status("OPENAI_API_KEY not set"))
        XCTAssertEqual(d(#"{"kind":"transcript","role":"user","text":"hi"}"#), .transcript(role: "user", text: "hi"))
        XCTAssertEqual(d(#"{"kind":"error","text":"boom"}"#), .error("boom"))
        XCTAssertEqual(d(#"{"kind":"mic","text":"start"}"#), .mic(start: true))
        XCTAssertEqual(d(#"{"kind":"mic","text":"stop"}"#), .mic(start: false))
        XCTAssertEqual(d(#"{"kind":"metrics","text":"{\"a\":1}"}"#), .metrics(#"{"a":1}"#))
    }

    func testMouthDefaultsToZeroWhenLevelOmitted() {
        XCTAssertEqual(d(#"{"kind":"mouth"}"#), .mouth(0))
    }

    func testUnknownKindAndGarbageAreNil() {
        XCTAssertNil(d(#"{"kind":"nope"}"#))
        XCTAssertNil(d("not json"))
    }

    func testConfigEncodesCamelCaseKeys() throws {
        let c = EngineConfig(apiKey: "k", model: "gpt-realtime", voice: "marin", personaPath: nil,
                             idleTimeoutSecs: 7, pack: "bit", packRoots: ["/x"], noisyRoom: false)
        let json = String(decoding: try JSONEncoder().encode(c), as: UTF8.self)
        XCTAssertTrue(json.contains("\"idleTimeoutSecs\":7"), json)
        XCTAssertTrue(json.contains("\"packRoots\""), json)
        XCTAssertTrue(json.contains("\"apiKey\""), json)
        XCTAssertFalse(json.contains("_"), json)
    }
}
