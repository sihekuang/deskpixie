import XCTest
@testable import DeskPixieCore

/// V-027: the Mic Mode picker behind a seam, so the tray action is testable without opening
/// Control Center.
@MainActor
final class MicModesSeamTests: XCTestCase {
    private final class FakePicker: MicModeSystem {
        var hasPicker: Bool
        var shown = 0
        init(hasPicker: Bool) { self.hasPicker = hasPicker }
        func showPicker() { shown += 1 }
    }

    func testShowOpensThePickerWhereAvailable() {
        let f = FakePicker(hasPicker: true)
        XCTAssertTrue(MicModes.show(using: f))
        XCTAssertEqual(f.shown, 1)
    }

    func testShowDoesNothingWithoutThePicker() {
        let f = FakePicker(hasPicker: false)
        XCTAssertFalse(MicModes.show(using: f))
        XCTAssertEqual(f.shown, 0)
    }

    func testTheTrayRowFollowsAvailability() {
        let s = try! JSONDecoder().decode(TrayAppState.self, from: Data(#"{"state":"idle"}"#.utf8))
        let on = TrayMenu.build(s, localStatusLines: [], micModes: FakePicker(hasPicker: true))
        let off = TrayMenu.build(s, localStatusLines: [], micModes: FakePicker(hasPicker: false))
        XCTAssertTrue(on.contains(.action(.init(id: "mic-mode", title: "Mic Mode (Voice Isolation)…"))))
        XCTAssertFalse(off.contains(.action(.init(id: "mic-mode", title: "Mic Mode (Voice Isolation)…"))))
    }

    func testTheSystemSeamMatchesAVCaptureDevice() {
        XCTAssertEqual(AVMicModeSystem().hasPicker, MicModes.available)
    }
}
