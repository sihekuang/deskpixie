import XCTest
@testable import DeskPixieCore

/// #225: Settings ▸ Advanced ▸ Export Logs….
@MainActor
final class LogExportTests: XCTestCase {
    func test_advanced_has_the_export_button() {
        let rows = SettingsFixture.extras(on: .advanced)
        XCTAssertTrue(rows.contains {
            if case .button(_, _, let title, let action) = $0 { return title == "Export Logs…" && action == .exportLogs }
            return false
        }, "\(rows)")
    }

    func test_the_file_is_named_after_the_day_and_time() {
        var c = DateComponents()
        (c.year, c.month, c.day, c.hour, c.minute) = (2026, 10, 21, 14, 2)
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "America/Los_Angeles")!
        let date = cal.date(from: c)!
        XCTAssertEqual(SettingsLayout.logExportName(date, timeZone: cal.timeZone), "DeskPixie logs 2026-10-21 1402.zip")
    }

    func test_export_passes_where_and_the_versions() {
        let fake = FakeSettingsClient()
        let m = SettingsModel(client: fake)
        m.load()
        XCTAssertTrue(m.exportLogs(to: "/tmp/DeskPixie logs.zip", app: "0.10.0 (42)", os: "macOS 15.6"))
        XCTAssertEqual(fake.exports.count, 1)
        XCTAssertEqual(fake.exports.first?.path, "/tmp/DeskPixie logs.zip")
        XCTAssertEqual(fake.exports.first?.app, "0.10.0 (42)")
        XCTAssertNil(m.notice)
    }

    func test_a_failed_export_says_so() {
        let fake = FakeSettingsClient()
        fake.exportThrows = DeskPixieEngineError.callFailed("Couldn't save the logs: disk full")
        let m = SettingsModel(client: fake)
        m.load()
        XCTAssertFalse(m.exportLogs(to: "/tmp/x.zip", app: "1", os: "macOS"))
        XCTAssertEqual(m.notice, "Couldn't save the logs: disk full")
    }
}
