import XCTest
@testable import DeskPixieCore

final class AppSupportDirectoryTests: XCTestCase {
    let home = URL(fileURLWithPath: "/Users/fake-user", isDirectory: true)

    func testDefaultsToLibraryApplicationSupportUnderHome() {
        let resolved = AppSupportDirectory.resolve(home: home, environment: [:])
        XCTAssertEqual(resolved.path, "/Users/fake-user/Library/Application Support/\(DataMigration.newFolder)")
    }

    func testOverrideReplacesTheDefaultEntirely() {
        let resolved = AppSupportDirectory.resolve(home: home, environment: ["DESKPIXIE_SUPPORT_DIR": "/tmp/dp-e2e-run-1"])
        XCTAssertEqual(resolved.path, "/tmp/dp-e2e-run-1")
    }

    func testEmptyOverrideFallsBackToTheDefault() {
        let resolved = AppSupportDirectory.resolve(home: home, environment: ["DESKPIXIE_SUPPORT_DIR": ""])
        XCTAssertEqual(resolved.path, "/Users/fake-user/Library/Application Support/\(DataMigration.newFolder)")
    }
}
