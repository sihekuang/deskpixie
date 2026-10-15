import XCTest
@testable import DeskPixieCore

/// Settings → Agent → pix command (#89): a symlink to the app's bundled pix.
final class PixInstallerTests: XCTestCase {
    private var home: URL!
    private var bundled: URL!
    private let fm = FileManager.default

    override func setUpWithError() throws {
        home = fm.temporaryDirectory.appendingPathComponent("pix-\(UUID().uuidString)")
        try fm.createDirectory(at: home, withIntermediateDirectories: true)
        bundled = home.appendingPathComponent("DeskPixie.app/Contents/Helpers/pix")
        try fm.createDirectory(at: bundled.deletingLastPathComponent(), withIntermediateDirectories: true)
        fm.createFile(atPath: bundled.path, contents: Data("#!/bin/sh\n".utf8))
    }

    override func tearDownWithError() throws { try? fm.removeItem(at: home) }

    private func installer(path: String = "") -> PixInstaller {
        PixInstaller(home: home, bundled: bundled, path: path)
    }

    func testInstallCreatesASymlinkToTheBundledPix() throws {
        let p = installer()
        XCTAssertEqual(p.state(), .notInstalled)
        try p.install()
        XCTAssertEqual(p.state(), .installed)
        XCTAssertEqual(try fm.destinationOfSymbolicLink(atPath: p.link.path), bundled.path)
        try p.install() // again: nothing to do
        XCTAssertEqual(p.state(), .installed)
    }

    func testBrokenWhenTheAppMovedAndInstallFixesIt() throws {
        let p = installer()
        try p.install()
        try fm.removeItem(at: bundled)
        XCTAssertEqual(p.state(), .broken)
        fm.createFile(atPath: bundled.path, contents: Data())
        try p.install()
        XCTAssertEqual(p.state(), .installed)
    }

    func testALinkToAnotherCopyOfTheAppIsReplaced() throws {
        let p = installer()
        try fm.createDirectory(at: p.link.deletingLastPathComponent(), withIntermediateDirectories: true)
        try fm.createSymbolicLink(atPath: p.link.path, withDestinationPath: "/Applications/Old/DeskPixie.app/Contents/Helpers/pix")
        XCTAssertEqual(p.state(), .broken)
        try p.install()
        XCTAssertEqual(p.state(), .installed)
    }

    func testSomeoneElsesFileIsLeftAlone() throws {
        let p = installer()
        try fm.createDirectory(at: p.link.deletingLastPathComponent(), withIntermediateDirectories: true)
        fm.createFile(atPath: p.link.path, contents: Data("mine".utf8))
        guard case .other = p.state() else { return XCTFail("expected .other, got \(p.state())") }
        XCTAssertThrowsError(try p.install())
        XCTAssertThrowsError(try p.uninstall())
        XCTAssertEqual(fm.contents(atPath: p.link.path), Data("mine".utf8))
    }

    func testUninstallRemovesOnlyTheLink() throws {
        let p = installer()
        try p.install()
        try p.uninstall()
        XCTAssertEqual(p.state(), .notInstalled)
        XCTAssertTrue(fm.fileExists(atPath: bundled.path))
        try p.uninstall() // again: nothing to do
    }

    func testOnPath() {
        XCTAssertTrue(installer(path: "/usr/bin:\(home.path)/.local/bin").onPath)
        XCTAssertTrue(installer(path: "\(home.path)/.local/bin/").onPath)
        XCTAssertFalse(installer(path: "/usr/bin:/bin").onPath)
    }

    func testModelReportsStateAndErrors() throws {
        let m = PixCommandModel(installer: installer(path: "/usr/bin"))
        XCTAssertEqual(m.stateText, "Not installed")
        XCTAssertFalse(m.onPath)
        m.install()
        XCTAssertEqual(m.state, .installed)
        XCTAssertEqual(m.stateText, "Installed at ~/.local/bin/pix")
        XCTAssertNil(m.error)
        m.uninstall()
        XCTAssertEqual(m.state, .notInstalled)

        let p = installer()
        try fm.createDirectory(at: p.link.deletingLastPathComponent(), withIntermediateDirectories: true)
        fm.createFile(atPath: p.link.path, contents: Data("mine".utf8))
        let m2 = PixCommandModel(installer: p)
        m2.install()
        XCTAssertNotNil(m2.error)
        XCTAssertTrue(m2.stateText.contains("left alone"), m2.stateText)
    }
}
