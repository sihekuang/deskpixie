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
        guard case .captions(let on, let secs)? = d(#"{"kind":"captions","always":true,"dismissSecs":120}"#) else {
            return XCTFail("captions")
        }
        XCTAssertTrue(on)
        XCTAssertEqual(secs, 120)
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

    /// #231: a done line that only repeats what the box already shows (a hand-off) ends it as it
    /// is, without typing it out again.
    func test_a_done_line_repeating_the_shown_words_ends_without_retyping() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("working", "Passed on to your session on api."))
        m.apply(line("done", "Passed on to your session on api."))
        XCTAssertEqual(m.phase, .done)
        XCTAssertEqual(m.shown, "Passed on to your session on api.")
        XCTAssertFalse(m.showsStop)
        XCTAssertNotNil(m.ago(now: Date()))
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

    /// Review #3: Stop while only typing out cancels nothing in the core.
    func test_stop_says_whether_work_must_be_cancelled() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        XCTAssertTrue(m.stopped(), "thinking: the coordinator is working")
        var a = QuickChatModel()
        a.sent(job: "job_1")
        a.apply(line("question", "Which repo?"))
        XCTAssertFalse(a.stopped(), "typing out a question: nothing to cancel")
    }

    /// Review #4: a line that comes before chat.send's reply is kept and shown.
    func test_a_line_before_the_send_reply_is_not_lost() {
        var m = QuickChatModel()
        m.sending()
        XCTAssertEqual(m.phase, .thinking)
        XCTAssertTrue(m.showsStop)
        m.apply(line("done", "Fast answer.", job: "job_7"))
        XCTAssertEqual(m.phase, .thinking)
        m.sent(job: "job_7")
        XCTAssertEqual(m.phase, .answering)
        while m.tick() {}
        XCTAssertEqual(m.shown, "Fast answer.")
    }

    /// Review #4: answering a question reuses its job id; the early reply survives too.
    func test_an_early_reply_to_an_answered_question_survives() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("question", "Which repo?"))
        while m.tick() {}
        m.sending()
        m.apply(line("done", "Fixed it.", job: "job_1"))
        m.sent(job: "job_1")
        while m.tick() {}
        XCTAssertEqual(m.shown, "Fixed it.")
        XCTAssertEqual(m.phase, .done)
    }

    /// Stop before chat.send answers: it stays stopped, and the job is cancelled once known.
    func test_stop_before_the_job_is_known_cancels_it_when_it_is() {
        var m = QuickChatModel()
        m.sending()
        XCTAssertFalse(m.stopped(), "no job to cancel yet")
        XCTAssertEqual(m.phase, .stopped)
        XCTAssertTrue(m.sent(job: "job_3"), "now cancel it")
        XCTAssertEqual(m.phase, .stopped)
        m.apply(line("failed", "The user cancelled the job.", job: "job_3"))
        XCTAssertEqual(m.phase, .stopped)
        var n = QuickChatModel()
        n.sending()
        XCTAssertFalse(n.sent(job: "job_4"))
    }

    // MARK: - captions (#224)

    /// Voice mode: what Pixie says shows as a caption, typed out, with no Stop or status.
    func test_voice_caption_types_out_without_stop() {
        var m = QuickChatModel()
        XCTAssertTrue(m.caption(role: "assistant", text: "It is sunny today."))
        XCTAssertTrue(m.showsCaption)
        XCTAssertFalse(m.showsStop)
        XCTAssertNil(m.statusText)
        XCTAssertEqual(m.phase, .answering)
        while m.tick() {}
        XCTAssertEqual(m.shown, "It is sunny today.")
        XCTAssertEqual(m.phase, .done)
    }

    /// Only Pixie's side, and only words: your words show as they're heard, not from the final
    /// transcript, which can come after Pixie's reply (#251 review).
    func test_caption_ignores_the_user_and_empty_text() {
        var m = QuickChatModel()
        XCTAssertFalse(m.caption(role: "user", text: "what's the weather"))
        XCTAssertFalse(m.caption(role: "assistant", text: "   "))
        XCTAssertFalse(m.heard("  "))
        XCTAssertFalse(m.showsCaption)
    }

    /// #251: while you talk, your words so far replace Pixie's last reply, all at once (no typing
    /// out), and follow you as you go on.
    func test_your_words_show_live_over_pixies_last_reply() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "It is sunny today.")
        while m.tick() {}
        XCTAssertTrue(m.heard("what about"))
        XCTAssertTrue(m.isYou)
        XCTAssertTrue(m.showsCaption)
        XCTAssertEqual(m.shown, "what about")
        XCTAssertEqual(m.phase, .done)
        XCTAssertFalse(m.tick(), "nothing to type out")
        XCTAssertTrue(m.heard("what about tomorrow?"))
        XCTAssertEqual(m.shown, "what about tomorrow?")
    }

    /// Your words stay until Pixie answers; the reply then replaces them, typed out, and your
    /// final transcript arriving after it leaves the reply be.
    func test_your_words_stay_until_pixie_answers() {
        var m = QuickChatModel()
        m.heard("what about tomorrow?")
        XCTAssertTrue(m.caption(role: "assistant", text: "Rain tomorrow."))
        XCTAssertFalse(m.caption(role: "user", text: "What about tomorrow?"))
        XCTAssertFalse(m.isYou)
        XCTAssertEqual(m.shown, "")
        while m.tick() {}
        XCTAssertEqual(m.shown, "Rain tomorrow.")
    }

    /// #253: Pixie's words show as it says them, all at once, replacing yours; its final
    /// transcript then completes them without typing them out again.
    func test_pixies_words_show_as_it_speaks() {
        var m = QuickChatModel()
        m.heard("what's the weather?")
        XCTAssertTrue(m.saying("It's"))
        XCTAssertFalse(m.isYou)
        XCTAssertTrue(m.showsCaption)
        XCTAssertEqual(m.shown, "It's")
        XCTAssertTrue(m.saying("It's sunny"))
        XCTAssertEqual(m.shown, "It's sunny")
        XCTAssertFalse(m.tick(), "nothing to type out")
        XCTAssertTrue(m.caption(role: "assistant", text: "It's sunny today."))
        XCTAssertEqual(m.shown, "It's sunny today.")
        XCTAssertEqual(m.phase, .done)
    }

    /// You talk again before Pixie's final transcript comes: it doesn't cover your words.
    func test_pixies_late_final_words_dont_cover_yours() {
        var m = QuickChatModel()
        m.saying("It's sunny")
        m.heard("and tomorrow?")
        XCTAssertFalse(m.caption(role: "assistant", text: "It's sunny today."))
        XCTAssertTrue(m.isYou)
        XCTAssertEqual(m.shown, "and tomorrow?")
        // The next reply that isn't streamed is typed out as before.
        XCTAssertTrue(m.caption(role: "assistant", text: "Rain."))
        XCTAssertEqual(m.shown, "")
    }

    /// Text mode: your spoken words never cover a typed message still at work.
    func test_text_mode_keeps_a_typed_message_over_your_words() {
        var m = QuickChatModel()
        m.mode = "text"
        m.sending()
        XCTAssertFalse(m.heard("hello"))
        XCTAssertFalse(m.saying("Hi"))
        XCTAssertFalse(m.isYou)
    }

    /// A new reply replaces the last; the conversation ending puts the caption away.
    func test_a_new_caption_replaces_and_the_end_keeps_it_for_hover() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "First.")
        while m.tick() {}
        m.caption(role: "assistant", text: "Second one.")
        XCTAssertEqual(m.shown, "")
        while m.tick() {}
        XCTAssertEqual(m.shown, "Second one.")
        m.conversationEnded()
        XCTAssertFalse(m.showsCaption, "no longer live")
        XCTAssertEqual(m.shown, "Second one.")
        XCTAssertFalse(m.showsCard(hovering: false, always: false))
        XCTAssertTrue(m.showsCard(hovering: true, always: false), "hover brings it back (#224)")
        XCTAssertTrue(m.showsCard(hovering: false, always: true), "always keeps it up")
    }

    /// The conversation ending while it types out shows the rest at once.
    func test_the_end_finishes_a_caption_still_typing() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "One two three.")
        _ = m.tick()
        m.conversationEnded()
        XCTAssertEqual(m.shown, "One two three.")
        XCTAssertEqual(m.phase, .done)
    }

    /// Nothing said yet: hover and always show nothing.
    func test_nothing_said_shows_no_card() {
        let m = QuickChatModel()
        XCTAssertFalse(m.showsCard(hovering: true, always: true))
    }

    /// A typed message's answer is also what hover shows, once it is done.
    func test_hover_shows_a_typed_answer_once_done() {
        var m = QuickChatModel()
        m.mode = "text"
        m.sent(job: "job_1")
        XCTAssertFalse(m.showsCard(hovering: true, always: false), "still thinking")
        m.apply(line("done", "Two are running."))
        while m.tick() {}
        XCTAssertTrue(m.showsCard(hovering: true, always: false))
    }

    /// How long ago it was said, once it is all shown.
    func test_ago() {
        let t0 = Date(timeIntervalSince1970: 1_000_000)
        var m = QuickChatModel()
        XCTAssertNil(m.ago(now: t0))
        m.caption(role: "assistant", text: "Hi.", now: t0)
        XCTAssertNil(m.ago(now: t0), "not while typing out")
        while m.tick() {}
        XCTAssertEqual(m.ago(now: t0.addingTimeInterval(20)), "just now")
        XCTAssertEqual(m.ago(now: t0.addingTimeInterval(125)), "2m ago")
        XCTAssertEqual(m.ago(now: t0.addingTimeInterval(3 * 3600 + 5)), "3h ago")
        XCTAssertEqual(m.ago(now: t0.addingTimeInterval(2 * 86400)), "2d ago")
    }

    /// Text mode: captions show in the same place, but never over a typed message at work.
    func test_text_mode_caption_waits_for_a_typed_message() {
        var m = QuickChatModel()
        m.mode = "text"
        XCTAssertTrue(m.caption(role: "assistant", text: "Hello."))
        XCTAssertTrue(m.showsCaption)
        m.sending()
        XCTAssertFalse(m.showsCaption)
        XCTAssertTrue(m.showsStop)
        XCTAssertFalse(m.caption(role: "assistant", text: "Not now."))
        XCTAssertEqual(m.phase, .thinking)
        m.sent(job: "job_1")
        m.apply(line("done", "Done it."))
        XCTAssertFalse(m.caption(role: "assistant", text: "Still typing."))
        while m.tick() {}
        XCTAssertTrue(m.caption(role: "assistant", text: "Now fine."))
    }

    /// In text mode the conversation ending leaves the box as it is.
    func test_text_mode_keeps_the_caption_when_the_conversation_ends() {
        var m = QuickChatModel()
        m.mode = "text"
        m.caption(role: "assistant", text: "Kept.")
        while m.tick() {}
        m.conversationEnded()
        XCTAssertEqual(m.shown, "Kept.")
    }

    // MARK: - putting it away (#224)

    /// The ✕: the words go, hover and always show nothing until something new is said.
    func test_dismiss_puts_the_words_away() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "Hi.")
        while m.tick() {}
        m.conversationEnded()
        XCTAssertTrue(m.canDismiss)
        m.dismiss()
        XCTAssertFalse(m.showsCard(hovering: true, always: true))
        XCTAssertFalse(m.canDismiss)
        XCTAssertNil(m.ago(now: Date()))
        XCTAssertTrue(m.caption(role: "assistant", text: "New."), "the next words show again")
    }

    /// No ✕ while a typed message is at work (Stop is there).
    func test_no_dismiss_while_a_typed_message_works() {
        var m = QuickChatModel()
        m.mode = "text"
        m.sent(job: "job_1")
        XCTAssertFalse(m.canDismiss)
        m.apply(line("done", "Done it."))
        XCTAssertFalse(m.canDismiss, "still typing out")
        while m.tick() {}
        XCTAssertTrue(m.canDismiss)
        m.dismiss()
        XCTAssertEqual(m.phase, .ready)
        XCTAssertEqual(m.shown, "")
    }

    /// Put away after the setting's seconds; 0 keeps it; never while the conversation goes on.
    func test_dismissed_after_the_setting() {
        let t0 = Date(timeIntervalSince1970: 1_000_000)
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "Hi.", now: t0)
        while m.tick() {}
        XCTAssertFalse(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(500)), "still live")
        m.conversationEnded()
        XCTAssertFalse(m.dismissIfOlder(than: 0, now: t0.addingTimeInterval(500)), "0 keeps it")
        XCTAssertFalse(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(119)))
        XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(120)))
        XCTAssertFalse(m.showsCard(hovering: true, always: true))
        XCTAssertFalse(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(500)), "nothing left")
    }

    /// #241: a session's answer can come minutes after the hand-off; it shows even if the card
    /// was put away (✕ or the timer) or a caption took its place, since nothing else says it.
    func test_a_late_answer_to_the_last_message_comes_back() {
        for putAway in ["dismiss", "caption"] {
            var m = QuickChatModel()
            m.mode = "text"
            m.sent(job: "job_1")
            m.apply(line("done", "Passed on to your session on api."))
            while m.tick() {}
            if putAway == "dismiss" {
                m.dismiss()
            } else {
                m.caption(role: "assistant", text: "Hello there.")
            }
            m.apply(line("done", "The redirect loop is fixed."))
            XCTAssertFalse(m.isCaption, putAway)
            XCTAssertEqual(m.job, "job_1", putAway)
            while m.tick() {}
            XCTAssertEqual(m.phase, .done, putAway)
            XCTAssertEqual(m.shown, "The redirect loop is fixed.", putAway)
        }
    }

    /// Only the last message's finished lines: not an older job's, and no working line.
    func test_a_put_away_card_ignores_other_jobs_and_working_lines() {
        var m = QuickChatModel()
        m.sent(job: "job_1")
        m.apply(line("done", "Passed on."))
        while m.tick() {}
        m.dismiss()
        m.apply(line("working", "Still at it."))
        XCTAssertEqual(m.phase, .ready)
        m.apply(line("done", "Old news.", job: "job_0"))
        XCTAssertEqual(m.phase, .ready)
    }
}

/// #240: attachments alone can be sent.
final class QuickChatAttachmentTests: XCTestCase {
    func testCanSendWithAttachmentsAndNoText() {
        let m = QuickChatModel()
        XCTAssertTrue(m.canSend("", attachments: 1))
        XCTAssertFalse(m.canSend("  ", attachments: 0))
        XCTAssertTrue(m.canSend("hi"))
        var busy = QuickChatModel()
        busy.sending()
        XCTAssertFalse(busy.canSend("", attachments: 1))
    }
}
