import XCTest
@testable import DeskPixieCore

final class DataMigrationTests: XCTestCase {
    var base: URL!
    let fm = FileManager.default
    var old: URL { base.appendingPathComponent(DataMigration.oldFolder) }
    var new: URL { base.appendingPathComponent(DataMigration.newFolder) }

    override func setUpWithError() throws {
        base = fm.temporaryDirectory.appendingPathComponent("dp-mig-\(UUID().uuidString)")
        try fm.createDirectory(at: base, withIntermediateDirectories: true)
    }
    override func tearDownWithError() throws { try? fm.removeItem(at: base) }

    private func write(_ s: String, _ rel: String, in dir: URL) throws {
        let u = dir.appendingPathComponent(rel)
        try fm.createDirectory(at: u.deletingLastPathComponent(), withIntermediateDirectories: true)
        try s.write(to: u, atomically: true, encoding: .utf8)
    }
    private func read(_ rel: String, in dir: URL) -> String? {
        try? String(contentsOf: dir.appendingPathComponent(rel), encoding: .utf8)
    }
    private func session(in dir: URL) throws -> String? {
        let d = try Data(contentsOf: dir.appendingPathComponent("settings.json"))
        let o = try JSONSerialization.jsonObject(with: d) as? [String: Any]
        return (o?["agent"] as? [String: Any])?["session"] as? String
    }

    func testPlainCopy() throws {
        try write("a", "mascots/x/pack.json", in: old)
        try write("b", "recordings/r.wav", in: old)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .migrated)
        XCTAssertEqual(read("mascots/x/pack.json", in: new), "a")
        XCTAssertEqual(read("recordings/r.wav", in: new), "b")
    }

    func testOldFolderKept() throws {
        try write("a", "canvas/c.md", in: old)
        _ = DataMigration.run(appSupport: base, pid: 1)
        XCTAssertEqual(read("canvas/c.md", in: old), "a")
    }

    func testEmptyNewFolderIsMigratedInto() throws {
        try write("a", "f.txt", in: old)
        try fm.createDirectory(at: new, withIntermediateDirectories: true)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .migrated)
        XCTAssertEqual(read("f.txt", in: new), "a")
    }

    func testNewFolderWithDataUntouched() throws {
        try write("a", "f.txt", in: old)
        try write("mine", "own.txt", in: new)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .skipped)
        XCTAssertEqual(read("own.txt", in: new), "mine")
        XCTAssertNil(read("f.txt", in: new))
    }

    func testNoOldFolder() throws {
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .skipped)
        XCTAssertFalse(fm.fileExists(atPath: new.path))
    }

    func testSecondRunIsNoOp() throws {
        try write("a", "f.txt", in: old)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .migrated)
        try write("changed", "f.txt", in: new)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .skipped)
        XCTAssertEqual(read("f.txt", in: new), "changed")
    }

    func testNoTempSiblingLeftBehind() throws {
        try write("a", "f.txt", in: old)
        _ = DataMigration.run(appSupport: base, pid: 42)
        let names = try fm.contentsOfDirectory(atPath: base.path)
        XCTAssertFalse(names.contains { $0.contains("migrating") })
    }

    func testSymlinksCopiedAsLinks() throws {
        try write("a", "real.txt", in: old)
        try fm.createSymbolicLink(atPath: old.appendingPathComponent("link.txt").path, withDestinationPath: "real.txt")
        _ = DataMigration.run(appSupport: base, pid: 1)
        let dest = try fm.destinationOfSymbolicLink(atPath: new.appendingPathComponent("link.txt").path)
        XCTAssertEqual(dest, "real.txt")
    }

    func testSessionRewrittenWhenAiMascot() throws {
        try write(#"{"agent":{"session":"ai-mascot","other":1},"keep":true}"#, "settings.json", in: old)
        _ = DataMigration.run(appSupport: base, pid: 1)
        XCTAssertEqual(try session(in: new), "deskpixie")
        let d = try Data(contentsOf: new.appendingPathComponent("settings.json"))
        let o = try JSONSerialization.jsonObject(with: d) as? [String: Any]
        XCTAssertEqual(o?["keep"] as? Bool, true)
        XCTAssertEqual((o?["agent"] as? [String: Any])?["other"] as? Int, 1)
        // original untouched
        XCTAssertEqual(try session(in: old), "ai-mascot")
    }

    func testCustomSessionKept() throws {
        try write(#"{"agent":{"session":"my-team"}}"#, "settings.json", in: old)
        _ = DataMigration.run(appSupport: base, pid: 1)
        XCTAssertEqual(try session(in: new), "my-team")
    }

    func testBrokenJSONLeftAlone() throws {
        try write("{ not json ai-mascot", "settings.json", in: old)
        XCTAssertEqual(DataMigration.run(appSupport: base, pid: 1), .migrated)
        XCTAssertEqual(read("settings.json", in: new), "{ not json ai-mascot")
    }
}
