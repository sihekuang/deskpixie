import XCTest
@testable import DeskPixieCore

/// #221: the text box under Pixie.
final class QuickChatModelTests: XCTestCase {
    private func line(_ status: String, _ text: String, job: String = "job_1") -> ChatLine {
        ChatLine(job: job, status: status, text: text)
    }

    func test_events_decode() {
        func d(_ s: String) -> EngineEvent? { EngineEvent.decode(Data(s.utf8)) }
        guard case .chatLine(let l)? = d(#"{"kind":"chat.line","job":"job_1","status":"done","text":"Two are running."}"#) else {
            return XCTFail("chat.line")
        }
        XCTAssertEqual(l, line("done", "Two are running."))
        guard case .chatFocus? = d(#"{"kind":"chat.focus"}"#) else { return XCTFail("chat.focus") }
        guard case .replyMode(let m)? = d(#"{"kind":"replyMode","mode":"text"}"#) else { return XCTFail("replyMode") }
        XCTAssertEqual(m, "text")
    }

    func test_send_thinks_with_stop_and_no_sending() {
        var m = QuickChatModel()
        XCTAssertEqual(m.phase, .ready)
        XCTAssertFalse(m.showsStop)
        XCTAssertTrue(m.canSend("hi"))
        XCTAssertFalse(m.canSend("   "))
        m.sent(job: "job_1")
        XCTAssertEqual(m.phase, .thinking)
        XCTAssertTrue(m.showsStop)
        XCTAssertEqual(m.statusText, "Thinking…")
        XCTAssertFalse(m.canSend("more"))
        XCTAssertEqual(m.shown, "")
    }

    func test_a_working_line_shows_and_keeps_thinking() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("working", "Looking now."))
        XCTAssertEqual(m.phase, .thinking)
        XCTAssertEqual(m.shown, "Looking now.")
        XCTAssertTrue(m.showsStop)
    }

    func test_a_done_line_types_out_word_by_word_then_stop_goes() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("done", "Two are running now."))
        XCTAssertEqual(m.phase, .answering)
        XCTAssertEqual(m.statusText, "Answering…")
        XCTAssertTrue(m.showsStop)
        XCTAssertTrue(m.tick())
        XCTAssertEqual(m.shown, "Two")
        XCTAssertTrue(m.tick())
        XCTAssertEqual(m.shown, "Two are")
        while m.tick() {}
        XCTAssertEqual(m.shown, "Two are running now.")
        XCTAssertEqual(m.phase, .done)
        XCTAssertFalse(m.showsStop)
        XCTAssertNil(m.statusText)
        XCTAssertTrue(m.canSend("next"))
    }

    func test_another_jobs_line_is_ignored() {
        var m = QuickChatModel()
        m.sent(job: "job_2")
        m.apply(line("done", "old answer", job: "job_1"))
        XCTAssertEqual(m.phase, .thinking)
        XCTAssertEqual(m.shown, "")
    }

    func test_a_question_or_failure_is_answered_too() {
        for status in ["question", "failed"] {
            var m = QuickChatModel()
            m.sent(job: "job_1")
            m.apply(line(status, "Which repo?"))
            while m.tick() {}
            XCTAssertEqual(m.phase, .done, status)
            XCTAssertEqual(m.shown, "Which repo?")
        }
    }

    func test_a_send_that_fails_shows_why() {
        var m = QuickChatModel()
        m.failedToSend("The coordinator is off.")
        XCTAssertEqual(m.phase, .done)
        XCTAssertEqual(m.shown, "The coordinator is off.")
        XCTAssertFalse(m.showsStop)
    }

    func test_stop_while_thinking_is_stopped() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("working", "Halfway."))
        m.stopped()
        XCTAssertEqual(m.phase, .stopped)
        XCTAssertEqual(m.shown, "Halfway.")
        XCTAssertFalse(m.showsStop)
        XCTAssertTrue(m.canSend("again"))
        m.apply(line("failed", "The user cancelled the job."))
        XCTAssertEqual(m.phase, .stopped, "the cancel's own line doesn't replace Stopped")
    }

    func test_stop_while_answering_shows_it_all() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("done", "one two three"))
        _ = m.tick()
        m.stopped()
        XCTAssertEqual(m.phase, .done)
        XCTAssertEqual(m.shown, "one two three")
    }

    func test_the_next_send_replaces_the_answer() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("done", "first"))
        while m.tick() {}
        m.sent(job: "job_2")
        XCTAssertEqual(m.shown, "")
        XCTAssertEqual(m.phase, .thinking)
    }
}
