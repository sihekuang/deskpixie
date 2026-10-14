import XCTest
@testable import DeskPixieCore

/// The updates bubble's view of `settings.snapshot` values (`notifications.*`).
final class BubbleSettingsValuesTests: XCTestCase {
    private func snapshot(_ values: String) -> Data {
        Data(#"{"platform":"macos","values":\#(values),"fields":[],"restartPending":[],"seq":3}"#.utf8)
    }

    func testReadsTheNotificationsBlock() throws {
        let s = try XCTUnwrap(BubbleSettingsValues.fromSnapshot(snapshot(
            #"{"scale":6,"notifications":{"debounceSecs":20,"bubble":{"enabled":false,"popSecs":9,"lines":3},"awayAfterMins":12}}"#)))
        XCTAssertEqual(s, BubbleSettingsValues(enabled: false, popSecs: 9, lines: 3, awayAfterMins: 12))
    }

    func testMissingValuesFallBackToTheDefaults() throws {
        XCTAssertEqual(BubbleSettingsValues.defaults,
                       BubbleSettingsValues(enabled: true, popSecs: 6, lines: 5, awayAfterMins: 5))
        XCTAssertEqual(BubbleSettingsValues.fromSnapshot(snapshot(#"{"scale":6}"#)), .defaults)
        XCTAssertEqual(BubbleSettingsValues.fromSnapshot(snapshot(#"{"notifications":{"bubble":{"popSecs":2}}}"#)),
                       BubbleSettingsValues(enabled: true, popSecs: 2, lines: 5, awayAfterMins: 5))
        XCTAssertNil(BubbleSettingsValues.fromSnapshot(Data("nope".utf8)))
    }

    func testMalformedOrOutOfRangeValuesFallBack() throws {
        let s = try XCTUnwrap(BubbleSettingsValues.fromSnapshot(snapshot(
            #"{"notifications":{"bubble":{"enabled":"yes","popSecs":0,"lines":99},"awayAfterMins":null}}"#)))
        XCTAssertEqual(s, .defaults)
    }
}
