import XCTest
@testable import DeskPixieCore

/// #263: Pixie's recent replies, to step back and forward through on the card.
final class ReplyHistoryTests: XCTestCase {
    private let t0 = Date(timeIntervalSince1970: 1_000_000)

    /// Three replies, `step` seconds apart.
    private func three(step: TimeInterval = 10) -> QuickChatModel {
        var m = QuickChatModel()
        m.historySecs = 300
        m.caption(role: "assistant", text: "One.", now: t0)
        m.caption(role: "assistant", text: "Two.", now: t0 + step)
        m.caption(role: "assistant", text: "Three.", now: t0 + 2 * step)
        return m
    }

    func test_replies_step_back_and_forward() {
        var m = three()
        XCTAssertEqual(m.position, .init(index: 3, count: 3))
        XCTAssertTrue(m.canBack)
        XCTAssertFalse(m.canForward)
        XCTAssertNil(m.viewed)

        m.back(now: t0 + 30)
        XCTAssertEqual(m.shownText, "Two.")
        XCTAssertEqual(m.position, .init(index: 2, count: 3))
        XCTAssertTrue(m.canForward)
        m.back(now: t0 + 30)
        XCTAssertEqual(m.shownText, "One.")
        XCTAssertFalse(m.canBack)
        m.back(now: t0 + 30)
        XCTAssertEqual(m.shownText, "One.", "nothing before the oldest")

        m.forward()
        m.forward()
        XCTAssertNil(m.viewed)
        XCTAssertEqual(m.shownText, m.shown, "back on the newest, as it goes on")
        XCTAssertEqual(m.position, .init(index: 3, count: 3))
    }

    /// One reply has nothing to step through: no arrows.
    func test_one_reply_has_no_position() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "Only.", now: t0)
        XCTAssertNil(m.position)
        XCTAssertFalse(m.canBack)
    }

    /// Only Pixie's replies are kept: your words and its words while you talk are not.
    func test_your_words_are_not_kept() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "One.", now: t0)
        m.heard("hi", now: t0 + 1)
        m.saying("Hello", now: t0 + 2)
        m.caption(role: "assistant", text: "Hello there.", now: t0 + 3)
        m.saying("Bye", now: t0 + 4)
        XCTAssertEqual(m.history.map(\.text), ["One.", "Hello there."])
    }

    /// While you look back a new reply leaves you there, and » offers the newest.
    func test_a_new_reply_while_looking_back_stays_and_offers_the_newest() {
        var m = three()
        m.back(now: t0 + 30)
        m.caption(role: "assistant", text: "Four.", now: t0 + 40)
        XCTAssertEqual(m.shownText, "Two.")
        XCTAssertTrue(m.newerWaiting)
        XCTAssertEqual(m.position, .init(index: 2, count: 4))

        m.toNewest()
        XCTAssertNil(m.viewed)
        XCTAssertFalse(m.newerWaiting)
        XCTAssertEqual(m.shownText, m.shown)

        m.back(now: t0 + 50)
        m.caption(role: "assistant", text: "Five.", now: t0 + 60)
        m.forward()
        m.forward()
        m.forward()
        XCTAssertNil(m.viewed)
        XCTAssertFalse(m.newerWaiting, "stepping forward to the end reaches it too")
    }

    /// Not looking back, a new reply shows as always, with no ».
    func test_a_new_reply_on_the_newest_just_shows() {
        var m = three()
        m.caption(role: "assistant", text: "Four.", now: t0 + 40)
        XCTAssertNil(m.viewed)
        XCTAssertFalse(m.newerWaiting)
        XCTAssertEqual(m.position, .init(index: 4, count: 4))
    }

    /// Replies older than the setting drop off, the one you look at too.
    func test_old_replies_drop_off() {
        var m = three(step: 200)
        XCTAssertEqual(m.history.map(\.text), ["Two."], "One. was 400 s old when Three. came")
        m.back(now: t0 + 450)
        XCTAssertEqual(m.shownText, "Two.")
        XCTAssertTrue(m.prune(now: t0 + 501))
        XCTAssertNil(m.viewed)
        XCTAssertNil(m.position)
        XCTAssertFalse(m.prune(now: t0 + 600), "nothing left to drop")
    }

    /// 0 keeps only the latest.
    func test_zero_keeps_only_the_latest() {
        var m = QuickChatModel()
        m.historySecs = 0
        m.caption(role: "assistant", text: "One.", now: t0)
        m.caption(role: "assistant", text: "Two.", now: t0 + 1)
        XCTAssertEqual(m.history, [])
        XCTAssertNil(m.position)
    }

    /// Typed answers are kept with their sessions; sending a message shows the newest again.
    func test_typed_answers_are_kept_and_sending_shows_the_newest() {
        var m = QuickChatModel()
        m.sending()
        m.sent(job: "job_1")
        m.apply(ChatLine(job: "job_1", status: "done", text: "Kiwis are green.", sessions: ["fruit-claude"]), now: t0)
        m.sending(now: t0 + 50)
        m.sent(job: "job_2")
        m.apply(ChatLine(job: "job_2", status: "done", text: "Bananas too."), now: t0 + 60)
        m.back(now: t0 + 70)
        XCTAssertEqual(m.shownText, "Kiwis are green.")
        XCTAssertEqual(m.shownSessions, ["fruit-claude"])
        XCTAssertEqual(m.ago(now: t0 + 180), "3m ago", "its own time")

        m.sending(now: t0 + 190)
        XCTAssertNil(m.viewed, "your new message")
        XCTAssertEqual(m.history.map(\.text), ["Kiwis are green.", "Bananas too."])
    }

    /// A working line ("Passed on…" while it runs) isn't a reply; its finished hand-off is.
    func test_a_working_line_is_not_kept() {
        var m = QuickChatModel()
        m.sending()
        m.sent(job: "job_1")
        m.apply(ChatLine(job: "job_1", status: "working", text: "Looking…"), now: t0)
        m.apply(ChatLine(job: "job_1", status: "done", text: "Passed on.", sessions: ["fruit-claude"]), now: t0 + 1)
        XCTAssertEqual(m.history, [])
        m.apply(ChatLine(job: "job_1", status: "done", text: "Done: kiwis are green.", sessions: ["fruit-claude"]), now: t0 + 30)
        XCTAssertEqual(m.history, [.init(text: "Passed on.", sessions: ["fruit-claude"], at: t0 + 1)])
    }

    /// The ✕ keeps the reply to step back to, and shows the newest when something comes.
    func test_dismiss_keeps_the_reply() {
        var m = three()
        m.back(now: t0 + 30)
        m.dismiss(now: t0 + 40)
        XCTAssertNil(m.viewed)
        XCTAssertEqual(m.history.map(\.text), ["One.", "Two.", "Three."])
    }

    /// Words aren't put away on their own while you look back.
    func test_no_put_away_while_looking_back() {
        var m = three()
        m.conversationEnded()
        m.back(now: t0 + 30)
        XCTAssertFalse(m.dismissIfOlder(than: 60, now: t0 + 500))
        m.toNewest()
        XCTAssertTrue(m.dismissIfOlder(than: 60, now: t0 + 500))
    }

    /// After ✕ or the put-away, the kept replies stay in reach: hovering shows "Earlier replies"
    /// with ‹ (no count), and stepping forward from the newest kept one goes back to that.
    func test_put_away_words_leave_the_history_in_reach() {
        var m = three()
        m.conversationEnded()
        XCTAssertTrue(m.dismissIfOlder(than: 60, now: t0 + 100))
        XCTAssertFalse(m.showsCard(hovering: false, always: true), "always-show doesn't bring them back")
        XCTAssertTrue(m.showsCard(hovering: true, always: false))
        XCTAssertTrue(m.showsEarlierOnly)
        XCTAssertNil(m.position)
        XCTAssertTrue(m.canBack)
        XCTAssertFalse(m.canForward)

        m.back(now: t0 + 110)
        XCTAssertEqual(m.shownText, "Three.")
        XCTAssertEqual(m.position, .init(index: 3, count: 3))
        XCTAssertFalse(m.showsEarlierOnly)
        m.back(now: t0 + 110)
        XCTAssertEqual(m.shownText, "Two.")
        m.forward()
        m.forward()
        XCTAssertTrue(m.showsEarlierOnly, "past the newest kept one: nothing current")

        XCTAssertTrue(m.prune(now: t0 + 1000))
        XCTAssertFalse(m.showsCard(hovering: true, always: false), "once they've dropped off, nothing")
        XCTAssertFalse(m.showsEarlierOnly)
    }

    /// The same words again while they're still typing out are the same reply, kept once.
    func test_a_repeat_while_typing_is_kept_once() {
        var m = QuickChatModel()
        m.historySecs = 300
        m.sending(now: t0)
        m.sent(job: "job_1")
        m.apply(ChatLine(job: "job_1", status: "question", text: "Apples or pears?"), now: t0 + 1)
        m.apply(ChatLine(job: "job_1", status: "done", text: "Apples or pears?"), now: t0 + 2)
        XCTAssertEqual(m.history, [])
        m.caption(role: "assistant", text: "Next.", now: t0 + 3)
        XCTAssertEqual(m.history.map(\.text), ["Apples or pears?"])
    }

    /// Why a message couldn't go isn't one of Pixie's replies.
    func test_a_failed_send_is_not_kept() {
        var m = QuickChatModel()
        m.historySecs = 300
        m.sending(now: t0)
        m.failedToSend("Pixie isn't running.")
        m.sending(now: t0 + 1)
        XCTAssertEqual(m.history, [])
    }
}
