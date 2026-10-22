import XCTest
@testable import DeskPixieCore

final class EngineEventTests: XCTestCase {
    private func d(_ s: String) -> EngineEvent? { EngineEvent.decode(Data(s.utf8)) }

    func testDecodeKinds() {
        XCTAssertEqual(d(#"{"kind":"state","state":"talking"}"#), .state("talking"))
        XCTAssertEqual(d(#"{"kind":"mouth","level":3}"#), .mouth(3))
        XCTAssertEqual(d(#"{"kind":"status","text":"OPENAI_API_KEY not set"}"#), .status("OPENAI_API_KEY not set"))
        XCTAssertEqual(d(#"{"kind":"transcript","role":"user","text":"hi"}"#), .transcript(role: "user", text: "hi"))
        XCTAssertEqual(d(#"{"kind":"heard","text":"hi Pix"}"#), .heard("hi Pix"))
        XCTAssertEqual(d(#"{"kind":"saying","text":"It's sun"}"#), .saying("It's sun"))
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

    func testShowPaneEventDecodes() {
        guard case .showPane(let r)? = d(#"{"kind":"showPane","job":"job_1","worker":"focus-fix"}"#) else {
            return XCTFail("not decoded")
        }
        XCTAssertEqual(r, PaneRequest(job: "job_1", worker: "focus-fix"))
        guard case .showPane(let c)? = d(#"{"kind":"showPane","job":"","worker":""}"#) else {
            return XCTFail("coordinator not decoded")
        }
        XCTAssertEqual(c, PaneRequest(job: "", worker: ""))
    }

    func testFocusPlanEventDecodes() {
        let json = #"{"kind":"focusPlan","plan":{"focus_urls":["warp://session/a"],"kill_pids":[611,621],"new_terminal":{"kind":"url","url":"warp://tab_config/x"}}}"#
        guard case .focusPlan(let plan)? = d(json) else { return XCTFail("not decoded") }
        XCTAssertEqual(plan.focusURLs, ["warp://session/a"])
        XCTAssertEqual(plan.killPIDs, [611, 621])
        XCTAssertEqual(plan.newTerminal, NewTerminalAction(kind: "url", url: "warp://tab_config/x"))
        XCTAssertNil(plan.raiseScript)
        XCTAssertNil(plan.itermSelect)
        XCTAssertNil(plan.activateApp)
    }

    func testUpdatesEventDecodes() {
        let json = #"{"kind":"updates","lines":[{"id":3,"text":"Done.","kind":"reply","at":1700000000000,"job":"job_000000000001"},{"id":4,"text":"hi","kind":"info","at":1700000000001}]}"#
        guard case .updates(let lines)? = EngineEvent.decode(Data(json.utf8)) else { return XCTFail() }
        XCTAssertEqual(lines.map(\.id), [3, 4])
        XCTAssertEqual(lines[0].job, "job_000000000001")
        XCTAssertEqual(lines[0].kind, "reply")
        XCTAssertEqual(lines[0].at, 1_700_000_000_000)
        XCTAssertNil(lines[1].job)
        XCTAssertNil(lines[1].worker)
    }

    func testEmptyUpdatesDecodes() {
        guard case .updates(let l)? = EngineEvent.decode(Data(#"{"kind":"updates","lines":[]}"#.utf8)) else { return XCTFail() }
        XCTAssertTrue(l.isEmpty)
        XCTAssertNil(d(#"{"kind":"updates"}"#), "lines is required")
    }

    func testDecodesAgents() {
        let json = #"{"kind":"agents","roster":{"agents":[{"name":"coordinator","label":"Coordinator","status":"idle","kind":"coordinator","reachable":true}],"yours":[{"name":"api-claude","label":"login bug","detail":"WebStorm","status":"working","kind":"session","reachable":true,"app":"com.jetbrains.WebStorm"}],"agent_count":0,"your_count":1}}"#
        guard case .agents(let v)? = EngineEvent.decode(Data(json.utf8)) else { return XCTFail() }
        XCTAssertEqual(v.agentCount, 0)
        XCTAssertEqual(v.yourCount, 1)
        XCTAssertEqual(v.yours.first?.app, "com.jetbrains.WebStorm")
        XCTAssertNil(v.agents.first?.detail)
        XCTAssertNil(EngineEvent.decode(Data(#"{"kind":"agents"}"#.utf8)), "roster is required")
    }

    func testMouthDefaultsToZeroWhenLevelOmitted() {
        XCTAssertEqual(d(#"{"kind":"mouth"}"#), .mouth(0))
    }

    func testUnknownKindAndGarbageAreNil() {
        XCTAssertNil(d(#"{"kind":"nope"}"#))
        XCTAssertNil(d("not json"))
    }

    func testConfigEncodesThePathsPayload() throws {
        let c = EngineConfig(appSupport: "/as", bundledPixies: "/res/pixies", designSystem: "/res/design-system",
                             dfn3ModelPath: "/res/DeepFilterNet3.tar.gz", packRoots: ["/res/pixies", "/as/pixies"])
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: JSONEncoder().encode(c)) as? [String: Any])
        XCTAssertEqual(obj["appSupport"] as? String, "/as")
        XCTAssertEqual(obj["bundledPixies"] as? String, "/res/pixies")
        XCTAssertEqual(obj["designSystem"] as? String, "/res/design-system")
        XCTAssertEqual(obj["dfn3ModelPath"] as? String, "/res/DeepFilterNet3.tar.gz")
        XCTAssertEqual(obj["platform"] as? String, "macos")
        // Transitional: the pre-P14a core reads packRoots; P14a ignores it.
        XCTAssertEqual(obj["packRoots"] as? [String], ["/res/pixies", "/as/pixies"])
        // Keys are Go's env/.env business now, never sent from Swift.
        XCTAssertNil(obj["apiKey"])
    }

    func testConfigForBundleUsesResourcesAndAppSupport() {
        let res = URL(fileURLWithPath: "/App.app/Contents/Resources")
        let support = URL(fileURLWithPath: "/Users/u/Library/Application Support/com.sihekuang.deskpixie")
        let c = EngineConfig.forBundle(resources: res, appSupport: support) { $0.hasSuffix("DeepFilterNet3.tar.gz") }
        XCTAssertEqual(c.appSupport, support.path)
        XCTAssertEqual(c.bundledPixies, "/App.app/Contents/Resources/pixies")
        XCTAssertEqual(c.designSystem, "/App.app/Contents/Resources/design-system")
        XCTAssertEqual(c.dfn3ModelPath, "/App.app/Contents/Resources/DeepFilterNet3.tar.gz")
        XCTAssertEqual(c.packRoots, [c.bundledPixies, support.path + "/pixies"])

        let noModel = EngineConfig.forBundle(resources: res, appSupport: support) { _ in false }
        XCTAssertEqual(noModel.dfn3ModelPath, "", "a build without libdf has no model; Go falls back to Hush/pass-through")
    }
}
