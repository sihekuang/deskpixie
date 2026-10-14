import CoreGraphics
import Foundation
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot ui/mascot/test/player-core.test.js (player + loadSequencer cases;
/// the gesture cases live in GestureTests). Same fixture pack, decoded from the same JSON.
let testPackJSON = #"""
{
  "name": "T",
  "cell": [32, 32],
  "fps": 8,
  "bob": { "px": 1, "frames": 2 },
  "states": {
    "idle": { "row": 0, "frames": 3, "loop": true, "bob": true, "faceDy": [0, 0, 0] },
    "listening": { "row": 1, "frames": 2, "loop": true, "bob": true, "faceDy": [0, 0] },
    "happy": { "row": 4, "frames": 3, "loop": false, "bob": false, "faceDy": [0, -2, -1] }
  },
  "mouth": { "row": 5, "anchor": [12, 20], "size": [8, 5], "levels": 4 }
}
"""#

// swiftlint:disable:next force_try
let testPack: Pack = try! JSONDecoder().decode(Pack.self, from: Data(testPackJSON.utf8))

final class MascotPlayerTests: XCTestCase {
    private let pack = testPack

    private func run(_ p: MascotPlayer, _ n: Int) -> MascotPlayer {
        var p = p
        for _ in 0..<n { p = p.stepped(pack: pack) }
        return p
    }

    private func player(_ state: String, frame: Int = 0, tick: Int = 0) -> MascotPlayer {
        var p = MascotPlayer(state: state)
        p.frame = frame
        p.tick = tick
        return p
    }

    func testLoopingStateWrapsToFrame0() {
        let p = run(MascotPlayer(), 3)
        XCTAssertEqual(p, player("idle", frame: 0, tick: 3))
    }

    func testNonLoopingStatePlaysOnceThenReturnsToIdle() {
        var p = MascotPlayer().setting("happy", pack: pack)
        p = run(p, 2)
        XCTAssertEqual(p.state, "happy")
        XCTAssertEqual(p.frame, 2)
        p = p.stepped(pack: pack)
        XCTAssertEqual(p.state, "idle")
        XCTAssertEqual(p.frame, 0)
    }

    func testSetStateIgnoresUnknownStates() {
        let p = MascotPlayer()
        XCTAssertEqual(p.setting("dancing", pack: pack), p)
    }

    func testSetStateToTheCurrentStateDoesNotRestartTheLoop() {
        let p = run(MascotPlayer(), 2)
        XCTAssertEqual(p.setting("idle", pack: pack), p)
    }

    func testSetStateSwitchesImmediatelyStartingAtFrame0() {
        let p = run(MascotPlayer(), 2).setting("listening", pack: pack)
        XCTAssertEqual(p.state, "listening")
        XCTAssertEqual(p.frame, 0)
    }

    func testBobAlternatesEveryBobFramesTicks() {
        let offs = (0...4).map { player("idle", tick: $0).bobOffset(pack: pack) }
        XCTAssertEqual(offs, [0, 0, 1, 1, 0])
    }

    func testBobIsOffForStatesWithBobFalse() {
        XCTAssertEqual(player("happy", tick: 2).bobOffset(pack: pack), 0)
    }

    func testBodyRectPicksColumnByFrameAndRowByState() {
        XCTAssertEqual(player("listening", frame: 1).bodyRect(pack: pack), CGRect(x: 32, y: 32, width: 32, height: 32))
    }

    func testMouthRectPicksTheLevelColumnInTheMouthRow() {
        XCTAssertEqual(MascotPlayer.mouthRect(level: 2, pack: pack), CGRect(x: 64, y: 160, width: 8, height: 5))
    }

    func testMouthLevelIsClampedToTheAvailableLevels() {
        XCTAssertEqual(MascotPlayer.mouthRect(level: 9, pack: pack).minX, 96)
        XCTAssertEqual(MascotPlayer.mouthRect(level: -3, pack: pack).minX, 0)
        // JS also checks NaN → 0; Swift levels are Int, so the extreme values stand in for it.
        XCTAssertEqual(MascotPlayer.mouthRect(level: Int.min, pack: pack).minX, 0)
        XCTAssertEqual(MascotPlayer.mouthRect(level: Int.max, pack: pack).minX, 96)
    }

    func testMouthPosFollowsFaceDy() {
        XCTAssertEqual(player("happy", frame: 1).mouthPos(pack: pack), CGPoint(x: 12, y: 18))
    }

    // MARK: loadSequencer

    func testAStateEventDuringAPackDecodeWinsOverThePayloadState() {
        var loads = LoadSequencer()
        let token = loads.begin(state: "idle")
        loads.noteState("thinking")
        XCTAssertEqual(loads.finish(token), "thinking")
    }

    func testALoadFinishingAfterANewerOneStartedIsDropped() {
        var loads = LoadSequencer()
        let first = loads.begin(state: "idle")
        let second = loads.begin(state: "listening")
        XCTAssertEqual(loads.finish(second), "listening")
        XCTAssertNil(loads.finish(first))
    }

    func testAnOlderLoadFinishingFirstIsDroppedAndTheNewerOneStillApplies() {
        var loads = LoadSequencer()
        let first = loads.begin(state: "idle")
        let second = loads.begin(state: "listening")
        XCTAssertNil(loads.finish(first))
        XCTAssertEqual(loads.finish(second), "listening")
    }

    func testThePayloadStateAppliesWhenNoStateEventArrivesDuringTheDecode() {
        var loads = LoadSequencer()
        loads.noteState("thinking") // before the load began: the payload is newer
        let token = loads.begin(state: "idle")
        XCTAssertEqual(loads.finish(token), "idle")
    }
}
