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

    func testDecodeParityKinds() {
        XCTAssertEqual(d(#"{"kind":"tray"}"#), .tray)
        XCTAssertEqual(d(#"{"kind":"settings.changed"}"#), .settingsChanged)
        XCTAssertEqual(d(#"{"kind":"restart"}"#), .restart)
        XCTAssertEqual(d(#"{"kind":"hotkey","combo":"Ctrl+Alt+Space"}"#), .hotkey(combo: "Ctrl+Alt+Space"))
        XCTAssertEqual(d(#"{"kind":"position","x":10.5,"y":-20}"#), .position(x: 10.5, y: -20))
        XCTAssertEqual(d(#"{"kind":"position"}"#), .position(x: nil, y: nil))
        XCTAssertEqual(d(#"{"kind":"devices","input":"in-uid","output":"out-uid"}"#),
                       .devices(input: "in-uid", output: "out-uid"))
        XCTAssertEqual(d(#"{"kind":"devices","output":"o"}"#), .devices(input: nil, output: "o"))
        XCTAssertEqual(d(#"{"kind":"notify","title":"Timer","body":"Done","sound":"glass"}"#),
                       .notify(title: "Timer", body: "Done", sound: "glass"))
        XCTAssertEqual(d(#"{"kind":"notify","title":"T","body":"B"}"#), .notify(title: "T", body: "B", sound: nil))
        XCTAssertEqual(d(#"{"kind":"canvas.open","url":"canvas://localhost/p/q3","title":"Q3"}"#),
                       .canvasOpen(url: "canvas://localhost/p/q3", title: "Q3"))
    }

    func testDecodePackEvent() throws {
        // The fixture is a dp_pack_json payload ({"pack":…,"sheetPath":…}); a pack event adds
        // kind, scale and hitMask ("AQID" = bytes 1, 2, 3).
        let url = try XCTUnwrap(Bundle.module.url(forResource: "bit-pack", withExtension: "json", subdirectory: "Fixtures"))
        let fixture = try Data(contentsOf: url)
        var obj = try XCTUnwrap(JSONSerialization.jsonObject(with: fixture) as? [String: Any])
        obj["kind"] = "pack"
        obj["sheetPath"] = "/a/sheet.png"
        obj["scale"] = 6
        obj["hitMask"] = "AQID"
        guard case .pack(let ev)? = EngineEvent.decode(try JSONSerialization.data(withJSONObject: obj)) else {
            return XCTFail("not a pack event")
        }
        XCTAssertEqual(ev.sheetPath, "/a/sheet.png")
        XCTAssertEqual(ev.scale, 6)
        XCTAssertEqual(ev.hitMask, Data([1, 2, 3]))
        XCTAssertEqual(ev.pack, PackPayload.decode(fixture)?.pack)
    }

    func testParityKindsWithMissingRequiredFieldsAreNil() {
        XCTAssertNil(d(#"{"kind":"hotkey"}"#))
        XCTAssertNil(d(#"{"kind":"canvas.open","title":"x"}"#))
        XCTAssertNil(d(#"{"kind":"notify","body":"x"}"#))
        XCTAssertNil(d(#"{"kind":"pack","sheetPath":"/x"}"#))
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
