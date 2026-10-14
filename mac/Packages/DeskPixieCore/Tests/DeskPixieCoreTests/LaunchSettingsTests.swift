import XCTest
@testable import DeskPixieCore

final class LaunchSettingsTests: XCTestCase {
    private func snap(_ values: String) -> Data {
        Data(#"{"platform":"macos","values":\#(values),"fields":[],"restartPending":[],"seq":1}"#.utf8)
    }

    func testReadsTheStoredHotkeyAndPinnedDevices() {
        let s = LaunchSettings.fromSnapshot(snap(#"{"hotkey":"cmd+shift+K","voice":{"inputDevice":"in-uid","outputDevice":null}}"#))
        XCTAssertEqual(s.hotkey, "cmd+shift+K")
        XCTAssertEqual(s.inputDevice, "in-uid")
        XCTAssertNil(s.outputDevice)
        XCTAssertEqual(s.platform, .macos)
    }

    func testFallsBackToConfigDefaults() {
        XCTAssertEqual(LaunchSettings.fromSnapshot(nil), .defaults)
        XCTAssertEqual(LaunchSettings.fromSnapshot(Data("not json".utf8)), .defaults)
        XCTAssertEqual(LaunchSettings.fromSnapshot(snap(#"{"hotkey":""}"#)).hotkey, "ctrl+alt+Space")
        XCTAssertEqual(LaunchSettings.defaults.hotkey, "ctrl+alt+Space")
    }

    func testEmptyDeviceMeansSystemDefault() {
        let s = LaunchSettings.fromSnapshot(snap(#"{"voice":{"inputDevice":"","outputDevice":"out"}}"#))
        XCTAssertNil(s.inputDevice)
        XCTAssertEqual(s.outputDevice, "out")
    }

    func testUnavailableLabelIsFormatted() {
        XCTAssertEqual(LaunchSettings.hotkeyUnavailable("ctrl+alt+Space", platform: .macos), "Hotkey unavailable: ⌃⌥Space")
    }

    func testCallArgsEncode() throws {
        func json(_ v: some Encodable) throws -> String {
            let e = JSONEncoder(); e.outputFormatting = .sortedKeys
            return String(decoding: try e.encode(v), as: UTF8.self)
        }
        XCTAssertEqual(try json(HotkeyFailedArgs(combo: "ctrl+K")), #"{"combo":"ctrl+K"}"#)
        XCTAssertEqual(try json(AudioFailedArgs(reason: "cannot start audio: x")), #"{"reason":"cannot start audio: x"}"#)
    }
}
