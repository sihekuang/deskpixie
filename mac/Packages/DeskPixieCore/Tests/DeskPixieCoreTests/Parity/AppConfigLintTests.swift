import XCTest

/// V-041: the app's static privacy config. Info.plist must explain the microphone and Apple
/// Events prompts; the entitlements must allow audio input and automation (hardened runtime).
final class AppConfigLintTests: XCTestCase {
    /// mac/ (this file is mac/Packages/DeskPixieCore/Tests/DeskPixieCoreTests/Parity/…).
    private let mac = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private func plist(_ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: mac.appendingPathComponent(path))
        return try XCTUnwrap(PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    func testInfoPlistHasBothUsageDescriptions() throws {
        let info = try plist("DeskPixie/Info.plist")
        for key in ["NSMicrophoneUsageDescription", "NSAppleEventsUsageDescription"] {
            let text = try XCTUnwrap(info[key] as? String, key)
            XCTAssertFalse(text.trimmingCharacters(in: .whitespaces).isEmpty, key)
        }
        XCTAssertEqual(info["LSUIElement"] as? Bool, true, "menu bar app")
    }

    func testEntitlementsAllowAudioInputAndAppleEvents() throws {
        let ent = try plist("DeskPixie/DeskPixie.entitlements")
        XCTAssertEqual(ent["com.apple.security.device.audio-input"] as? Bool, true)
        XCTAssertEqual(ent["com.apple.security.automation.apple-events"] as? Bool, true)
    }

    func testProjectUsesThesePlistsWithTheHardenedRuntime() throws {
        let yml = try String(contentsOf: mac.appendingPathComponent("project.yml"), encoding: .utf8)
        XCTAssertTrue(yml.contains("INFOPLIST_FILE: DeskPixie/Info.plist"))
        XCTAssertTrue(yml.contains("CODE_SIGN_ENTITLEMENTS: DeskPixie/DeskPixie.entitlements"))
        XCTAssertTrue(yml.contains("ENABLE_HARDENED_RUNTIME: YES"))
    }
}
