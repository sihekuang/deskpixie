import XCTest
@testable import DeskPixieCore

@MainActor
final class NotifierTests: XCTestCase {
    func testHandleNotifyPostsAndPlaysGlass() async {
        let sink = FakeSink()
        let n = Notifier(sink: sink)
        n.handle(.notify(title: "T", body: "B", sound: "glass"))
        await n.drain()
        XCTAssertEqual(sink.posted, ["T|B"])
        XCTAssertEqual(sink.played, 1)
    }
    func testNoSoundWhenNone() async {
        let sink = FakeSink()
        let n = Notifier(sink: sink)
        n.handle(.notify(title: "T", body: "B", sound: nil))
        n.handle(.notify(title: "T2", body: "B2", sound: "none"))
        await n.drain()
        XCTAssertEqual(sink.posted.count, 2)
        XCTAssertEqual(sink.played, 0)
    }
    func testForegroundNotificationsShowAsBanners() {
        XCTAssertTrue(ForegroundPresenter.options.contains(.banner))
        XCTAssertTrue(ForegroundPresenter.options.contains(.list))
        // The engine plays Glass itself; a second system sound would double it.
        XCTAssertFalse(ForegroundPresenter.options.contains(.sound))
    }
    /// D-477: the core's `notify` event (timer done, agent replies) → title, body and sound,
    /// as delivered through UNUserNotificationCenter (replaces Rust's osascript).
    func testNotifyEventMapsTitleBodyAndSound() async throws {
        let sink = FakeSink()
        let n = Notifier(sink: sink)
        let events = [
            #"{"kind":"notify","title":"Pixie","body":"Timer done","sound":"glass"}"#,
            #"{"kind":"notify","title":"Pixie","body":"Coordinator reply: tests pass"}"#,
            #"{"kind":"notify","title":"Pixie","body":"Tea","sound":"ping"}"#,
        ]
        for json in events { n.handle(try XCTUnwrap(EngineEvent.decode(Data(json.utf8)))) }
        await n.drain()
        XCTAssertEqual(sink.posted, ["Pixie|Timer done", "Pixie|Coordinator reply: tests pass", "Pixie|Tea"])
        XCTAssertEqual(sink.played, 1, "only sound \"glass\" plays the Glass chime")
    }

    func testSystemNotificationContent() {
        let c = SystemNotificationSink.content(title: "Pixie", body: "Timer done")
        XCTAssertEqual(c.title, "Pixie")
        XCTAssertEqual(c.body, "Timer done")
        XCTAssertNil(c.sound, "Glass is played separately, never the user's notification sound")
        XCTAssertEqual(SystemNotificationSink.glassPath, "/System/Library/Sounds/Glass.aiff")
    }

    /// #199: a notification about a bubble line carries it, and says its short form.
    func testANotificationAboutALineCarriesItAndItsShortForm() async throws {
        let sink = FakeSink()
        let n = Notifier(sink: sink)
        let json = #"{"kind":"notify","title":"Pixie","body":"api is done: tests pass.","update":{"id":7,"text":"api is done: tests pass.","kind":"session","at":7,"session":"api-claude","title":"api","brief":"Tests pass."}}"#
        n.handle(try XCTUnwrap(EngineEvent.decode(Data(json.utf8))))
        await n.drain()
        XCTAssertEqual(sink.posted, ["api|Tests pass.|7"])
        XCTAssertEqual(NotificationText.of(title: "Pixie", body: "Done.", update: nil).title, "Pixie")
    }

    func testANotificationsLineRoundTripsThroughUserInfo() throws {
        let line = UpdateLine(id: 9, text: "t", kind: "session", at: 9, job: nil, worker: nil, session: "api-claude",
                              title: "api", brief: "b")
        let json = String(data: try JSONEncoder().encode(line), encoding: .utf8)!
        XCTAssertEqual(SystemNotificationSink.update(from: [SystemNotificationSink.updateKey: json]), line)
        XCTAssertNil(SystemNotificationSink.update(from: [:]))
        XCTAssertEqual(SystemNotificationSink.identifier(update: 9), "update-9")
    }

    func testIgnoresOtherEvents() async {
        let sink = FakeSink()
        let n = Notifier(sink: sink)
        n.handle(.mouth(3))
        await n.drain()
        XCTAssertTrue(sink.posted.isEmpty)
    }
}

@MainActor
private final class FakeSink: NotificationSink {
    var posted: [String] = []
    var played = 0
    func post(title: String, body: String) async { posted.append("\(title)|\(body)") }
    func post(title: String, body: String, update: UpdateLine) async { posted.append("\(title)|\(body)|\(update.id)") }
    func playGlass() { played += 1 }
}
