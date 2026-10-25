import XCTest
@testable import DeskPixieCore

/// A core that answers `app.about` with `info`, or fails; it records what it was asked.
private final class FakeAboutClient: AboutClient, @unchecked Sendable {
    let info: AboutInfo?
    private let lock = NSLock()
    private var _asked: [AboutArgs] = []
    var asked: [AboutArgs] { lock.withLock { _asked } }

    init(_ info: AboutInfo?) { self.info = info }

    struct Failed: LocalizedError { var errorDescription: String? { "the core is gone" } }

    func about(_ args: AboutArgs) throws -> AboutInfo {
        lock.withLock { _asked.append(args) }
        guard let info else { throw Failed() }
        return info
    }
}

@MainActor
final class AboutTests: XCTestCase {
    private let args = AboutArgs(app: "0.19.0 (412)", os: "macOS 15.1.0", pix: "/A/pix", path: "/usr/bin")

    func testDecodesTheCoresAnswer() throws {
        let json = #"{"rows":[{"label":"DeskPixie","value":"0.19.0 (412)"},{"label":"Core","value":"0.19.0 (abc1234)"}],"note":"Your terminal's pix isn't the app's.","warning":true,"text":"DeskPixie: 0.19.0 (412)\n"}"#
        let info = try JSONDecoder().decode(AboutInfo.self, from: Data(json.utf8))
        XCTAssertEqual(info.rows.map(\.label), ["DeskPixie", "Core"])
        XCTAssertEqual(info.note, "Your terminal's pix isn't the app's.")
        XCTAssertTrue(info.warning)
        // No note when everything matches (Go leaves it out).
        let quiet = try JSONDecoder().decode(AboutInfo.self, from: Data(#"{"rows":[],"text":""}"#.utf8))
        XCTAssertNil(quiet.note)
        XCTAssertFalse(quiet.warning)
    }

    func testSendsWhatOnlyTheAppKnows() throws {
        let data = try JSONEncoder().encode(args)
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: String])
        XCTAssertEqual(obj, ["app": "0.19.0 (412)", "os": "macOS 15.1.0", "pix": "/A/pix", "path": "/usr/bin"])
    }

    func testLoadShowsTheVersions() async {
        let info = AboutInfo(rows: [.init(label: "Core", value: "0.19.0 (abc1234)")], text: "Core: 0.19.0 (abc1234)\n")
        let client = FakeAboutClient(info)
        let model = AboutModel(client: client)
        XCTAssertNil(model.info)
        let a = args
        await model.load { a }
        XCTAssertEqual(model.info, info)
        XCTAssertNil(model.error)
        XCTAssertEqual(client.asked, [args])
    }

    func testAFailedLoadSaysSoAndKeepsWhatItHad() async {
        let model = AboutModel(client: FakeAboutClient(nil))
        let a = args
        await model.load { a }
        XCTAssertNil(model.info)
        XCTAssertEqual(model.error, "Couldn't read the versions: the core is gone")
    }

    func testTheAboutPageIsLastWithItsVersionsRow() throws {
        XCTAssertEqual(SettingsLayout.tabs.last?.section, .about)
        XCTAssertEqual(SettingsLayout.tabTitle(.about), "About")
        let row = try XCTUnwrap(SettingsLayout.extra("about"))
        XCTAssertEqual(row.label, "Versions")
        XCTAssertEqual(row.path, "extra:about")
    }
}
