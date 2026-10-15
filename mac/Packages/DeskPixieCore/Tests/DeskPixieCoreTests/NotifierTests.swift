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
    func playGlass() { played += 1 }
}
