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
