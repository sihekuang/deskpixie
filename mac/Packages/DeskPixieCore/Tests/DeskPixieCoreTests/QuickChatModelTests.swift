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
        guard case .captions(let on, let secs, let fade, _)? = d(#"{"kind":"captions","always":true,"dismissSecs":120,"fadeSecs":7}"#) else {
            return XCTFail("captions")
        }
        XCTAssertTrue(on)
        XCTAssertEqual(secs, 120)
        XCTAssertEqual(fade, 7)
        // An older core sends no fadeSecs: the default.
        guard case .captions(_, _, let old, _)? = d(#"{"kind":"captions","always":true,"dismissSecs":120}"#) else {
            return XCTFail("captions without fadeSecs")
        }
        XCTAssertEqual(old, 4)
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
        XCTAssertEqual(m.shownFull, "Two are running now.", "its links are found in all of it (#277)")
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

    // MARK: - two bubbles (#254)

    /// When the other side speaks, the last bubble moves up and the new one shows below it.
    func test_the_other_side_speaking_moves_the_last_bubble_up() {
        let t0 = Date()
        var m = QuickChatModel()
        m.heard("what's the weather?", now: t0)
        XCTAssertNil(m.earlier, "nothing before")
        m.heard("what's the weather tomorrow?", now: t0)
        XCTAssertNil(m.earlier, "the same speaker goes on in place")
        m.saying("Rain", now: t0.addingTimeInterval(1))
        XCTAssertEqual(m.earlier, QuickChatModel.Earlier(text: "what's the weather tomorrow?", isYou: true, at: t0.addingTimeInterval(1)))
        m.saying("Rain tomorrow.", now: t0.addingTimeInterval(2))
        XCTAssertEqual(m.earlier?.text, "what's the weather tomorrow?", "Pixie goes on in place")
        m.heard("thanks", now: t0.addingTimeInterval(3))
        XCTAssertEqual(m.earlier, QuickChatModel.Earlier(text: "Rain tomorrow.", isYou: false, at: t0.addingTimeInterval(3)))
    }

    /// The bubble on top stays 4 s, fades for half a second, then goes.
    func test_the_bubble_on_top_fades_after_four_seconds() {
        let t0 = Date()
        var m = QuickChatModel()
        m.heard("hi", now: t0)
        m.saying("Hello!", now: t0)
        XCTAssertEqual(m.earlierPhase(now: t0.addingTimeInterval(3.9)), .shown)
        XCTAssertEqual(m.earlierPhase(now: t0.addingTimeInterval(4.1)), .fading)
        XCTAssertEqual(m.earlierPhase(now: t0.addingTimeInterval(4.6)), .gone)
        XCTAssertEqual(QuickChatModel().earlierPhase(now: t0), .gone)
    }

    /// A streamed reply that never got its final words doesn't swallow the next conversation's.
    func test_an_unfinished_streamed_reply_ends_with_the_conversation() {
        var m = QuickChatModel()
        m.saying("It's sun")
        m.conversationEnded()
        m.heard("hi again")
        XCTAssertTrue(m.caption(role: "assistant", text: "Hello!"))
        XCTAssertFalse(m.isYou)
    }

    /// The last conversation's words, kept for hover, don't come back as the bubble on top.
    func test_a_kept_caption_doesnt_move_up() {
        var m = QuickChatModel()
        m.saying("Bye!")
        m.conversationEnded()
        m.heard("hi again")
        XCTAssertNil(m.earlier)
    }

    /// The setting (captionFadeSecs) sets how long it stays; 0 shows only the latest.
    func test_the_fade_time_is_the_setting() {
        let t0 = Date()
        var m = QuickChatModel()
        m.fadeSecs = 10
        m.heard("hi", now: t0)
        m.saying("Hello!", now: t0)
        XCTAssertEqual(m.earlierPhase(now: t0.addingTimeInterval(9)), .shown)
        XCTAssertEqual(m.earlierPhase(now: t0.addingTimeInterval(10.1)), .fading)
        m.fadeSecs = 0
        XCTAssertEqual(m.earlierPhase(now: t0), .gone)
        m.heard("bye", now: t0)
        XCTAssertNil(m.earlier, "only the latest")
    }

    /// A typed message clears both bubbles.
    func test_a_typed_message_clears_the_bubble_on_top() {
        var m = QuickChatModel()
        m.heard("hi")
        m.saying("Hello!")
        m.mode = "text"
        m.sending()
        XCTAssertNil(m.earlier)
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

    /// The ✕: the words go; always-show shows nothing until something new is said.
    func test_dismiss_puts_the_words_away() {
        var m = QuickChatModel()
        m.caption(role: "assistant", text: "Hi.")
        while m.tick() {}
        m.conversationEnded()
        XCTAssertTrue(m.canDismiss)
        m.dismiss()
        XCTAssertFalse(m.showsCard(hovering: false, always: true))
        XCTAssertTrue(m.showsEarlierOnly, "hover offers only the way back to them (#263)")
        XCTAssertEqual(m.shownText, "")
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
        XCTAssertFalse(m.showsCard(hovering: false, always: true))
        XCTAssertTrue(m.showsEarlierOnly, "hover offers only the way back to them (#263)")
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

    /// #259: a session's answer that comes once the box was closed (a click elsewhere, Escape, ✕)
    /// shows on its own, not only on hover; it goes after the put-away time like any answer.
    func test_a_late_answer_shows_on_its_own_once_the_box_is_closed() {
        for putAway in ["close", "dismiss"] {
            let t0 = Date()
            var m = QuickChatModel()
            m.mode = "text"
            m.open = true
            m.sent(job: "job_1")
            m.apply(line("done", "Passed on to your session on fruit-stand."), now: t0)
            while m.tick() {}
            m.open = false
            if putAway == "dismiss" { m.dismiss() }
            XCTAssertFalse(m.showsCard(hovering: false, always: false), putAway)
            m.apply(line("done", "The pears are ripe."), now: t0)
            XCTAssertTrue(m.showsCard(hovering: false, always: false), "\(putAway): while it types out")
            while m.tick() {}
            XCTAssertEqual(m.shown, "The pears are ripe.", putAway)
            XCTAssertTrue(m.showsCard(hovering: false, always: false), putAway)
            XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(120)), putAway)
            XCTAssertFalse(m.showsCard(hovering: false, always: false), putAway)
        }
    }

    /// An answer that came while the box was open was seen: closing the box puts it away as before.
    func test_an_answer_seen_in_the_open_box_goes_with_it() {
        var m = QuickChatModel()
        m.mode = "text"
        m.open = true
        m.sent(job: "job_1")
        m.apply(line("done", "Passed on."))
        m.apply(line("done", "The pears are ripe."))
        while m.tick() {}
        m.open = false
        XCTAssertFalse(m.showsCard(hovering: false, always: false))
        XCTAssertTrue(m.showsCard(hovering: true, always: false), "hover still shows it")
    }

    /// The coordinator's closing line repeats the hand-off just after the box closed: nothing new,
    /// so it doesn't come back on its own.
    func test_a_repeat_of_what_was_shown_doesnt_bring_it_back() {
        var m = QuickChatModel()
        m.mode = "text"
        m.open = true
        m.sent(job: "job_1")
        m.apply(line("done", "Passed on to your session."))
        while m.tick() {}
        m.open = false
        m.apply(line("done", "Passed on to your session."))
        XCTAssertFalse(m.showsCard(hovering: false, always: false))
    }

    /// #260 review: a late answer read in the reopened box goes with it; a working line after it,
    /// or an empty answer, doesn't keep a card up on its own.
    func test_a_late_answer_read_in_the_box_or_empty_or_working_doesnt_stay() {
        var m = QuickChatModel()
        m.mode = "text"
        m.open = true
        m.sent(job: "job_1")
        m.apply(line("done", "Passed on."))
        while m.tick() {}
        m.open = false
        m.apply(line("done", "The pears are ripe."))
        while m.tick() {}
        m.open = true
        m.open = false
        XCTAssertFalse(m.showsCard(hovering: false, always: false), "read in the box")
        m.apply(line("done", "Now the plums."))
        XCTAssertTrue(m.showsCard(hovering: false, always: false))
        m.apply(line("working", "Still at it."))
        XCTAssertFalse(m.showsCard(hovering: false, always: false), "a working line")
        var e = QuickChatModel()
        e.mode = "text"
        e.open = true
        e.sent(job: "job_1")
        e.apply(line("done", "Passed on."))
        while e.tick() {}
        e.open = false
        e.apply(line("done", ""))
        XCTAssertFalse(e.showsCard(hovering: false, always: false), "an empty answer")
    }

    /// Only a finished answer comes back on its own, and the next message ends it.
    func test_a_late_working_line_or_the_next_message_doesnt_show_it() {
        var m = QuickChatModel()
        m.mode = "text"
        m.open = true
        m.sent(job: "job_1")
        m.apply(line("done", "Passed on."))
        while m.tick() {}
        m.open = false
        m.apply(line("working", "Still at it."))
        XCTAssertFalse(m.showsCard(hovering: false, always: false))
        m.apply(line("done", "Done now."))
        XCTAssertTrue(m.showsCard(hovering: false, always: false))
        m.sending()
        m.sent(job: "job_2")
        m.open = false
        m.apply(line("done", "Done now.", job: "job_1"))
        XCTAssertFalse(m.showsCard(hovering: false, always: false), "not an older message's answer")
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

    // MARK: Update cards (#286)

    private func card(_ id: UInt64, _ text: String, count: Int = 1) -> ReplyCard {
        ReplyCard(id: id, text: text, count: count, lines: [id], sessions: [])
    }

    func testAnUpdateShowsOnItsOwnAndGoesIntoHistory() {
        var m = QuickChatModel()
        XCTAssertTrue(m.update(card(1, "**live-like:** done")))
        XCTAssertTrue(m.showsCard(hovering: false, always: false))
        XCTAssertEqual(m.shownFull, "**live-like:** done")
        _ = m.update(card(2, "**api:** done"))
        XCTAssertEqual(m.position, QuickChatModel.Position(index: 2, count: 2))
        XCTAssertEqual(m.history.map(\.id), [1])
    }

    func testARowJoiningTheShownCardReplacesItInPlace() {
        var m = QuickChatModel()
        _ = m.update(card(1, "**a:** one"))
        _ = m.update(card(1, "**a:** one\n**b:** two", count: 2))
        XCTAssertEqual(m.count, 2)
        XCTAssertEqual(m.cardID, 1)
        XCTAssertEqual(m.shownFull, "**a:** one\n**b:** two")
        XCTAssertNil(m.position) // still one card, nothing to step back to
    }

    func testAnUpdateNeverCoversATypedMessageAtWork() {
        var m = QuickChatModel()
        m.mode = "text"; m.open = true
        m.sending(); _ = m.sent(job: "job_1")
        XCTAssertFalse(m.update(card(1, "**a:** done")))
        XCTAssertEqual(m.phase, .thinking)
        XCTAssertEqual(m.history.last?.text, "**a:** done")
        XCTAssertTrue(m.newerWaiting)
        XCTAssertNil(m.cardID)
    }

    func testAReEmittedCardPushedToHistoryUpdatesThatEntry() {
        var m = QuickChatModel()
        _ = m.update(card(1, "**a:** one"))
        _ = m.update(card(2, "**b:** two"))
        XCTAssertFalse(m.update(card(1, "**a:** one\n**c:** three", count: 2)))
        XCTAssertEqual(m.history.map(\.id), [1])
        XCTAssertEqual(m.history.first?.text, "**a:** one\n**c:** three")
        XCTAssertEqual(m.history.first?.count, 2)
        XCTAssertEqual(m.cardID, 2)
        XCTAssertEqual(m.shownFull, "**b:** two")
    }

    func testAPutAwayCardGainingARowShowsAgain() {
        var m = QuickChatModel()
        let t0 = Date()
        _ = m.update(card(1, "**a:** one"), now: t0)
        m.dismiss(now: t0)
        XCTAssertFalse(m.showsCard(hovering: false, always: false))
        XCTAssertTrue(m.update(card(1, "**a:** one\n**b:** two", count: 2), now: t0 + 100))
        XCTAssertTrue(m.showsCard(hovering: false, always: false))
        XCTAssertEqual(m.cardID, 1)
        XCTAssertEqual(m.count, 2)
        XCTAssertEqual(m.history, [])
        XCTAssertFalse(m.dismissIfOlder(than: 120, now: t0 + 130))
        XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0 + 221))
    }

    func testTheHeaderCountsACombinedCard() {
        XCTAssertEqual(QuickChatModel.header(who: "Pixie", count: 1, ago: "2m ago"), "Pixie · 2m ago")
        XCTAssertEqual(QuickChatModel.header(who: "Pixie", count: 3, ago: "just now"), "3 updates · just now")
        XCTAssertEqual(QuickChatModel.header(who: "Pixie", count: 2, ago: nil), "2 updates")
        XCTAssertEqual(QuickChatModel.header(who: "You", count: 1, ago: nil), "You")
    }

    func testTheShownCountFollowsTheCardLookedAt() {
        var m = QuickChatModel()
        XCTAssertEqual(m.shownCount, 1)
        _ = m.update(card(1, "**a:** one\n**b:** two", count: 2))
        XCTAssertEqual(m.shownCount, 2)
        _ = m.update(card(2, "**c:** three"))
        XCTAssertEqual(m.shownCount, 1)
        m.back(now: Date())
        XCTAssertEqual(m.shownCount, 2)
    }

    /// #286 tryout: cards restored at launch, fed oldest first, leave the newest on top.
    func testCardsFedInOrderLeaveTheNewestOnTop() {
        var m = QuickChatModel()
        for i in UInt64(1)...3 { _ = m.update(card(i, "card \(i)")) }
        XCTAssertEqual(m.cardID, 3)
        XCTAssertEqual(m.shownFull, "card 3")
        XCTAssertEqual(m.position, QuickChatModel.Position(index: 3, count: 3))
    }

    /// #286 tryout: a card restored at launch says how long ago its row came, not "just now".
    func testACardsAgoIsWhenItsNewestRowCame() {
        var m = QuickChatModel()
        let t0 = Date()
        _ = m.update(ReplyCard(id: 1, text: "a", count: 1, lines: [1], at: t0.addingTimeInterval(-240)), now: t0)
        XCTAssertEqual(m.ago(now: t0), "4m ago")
        _ = m.update(ReplyCard(id: 2, text: "b", count: 1, lines: [2], at: t0.addingTimeInterval(-60)), now: t0)
        XCTAssertEqual(m.ago(now: t0), "1m ago")
        m.back(now: t0)
        XCTAssertEqual(m.ago(now: t0), "4m ago", "the kept card keeps its own time")
    }

    /// A gained row: the ago follows the newest row, while the dismiss time runs from the pop.
    func testAGainedRowMovesTheAgoButThePopRunsFromNow() {
        var m = QuickChatModel()
        let t0 = Date()
        _ = m.update(ReplyCard(id: 1, text: "a", count: 1, lines: [1], at: t0.addingTimeInterval(-300)), now: t0)
        XCTAssertEqual(m.ago(now: t0), "5m ago")
        _ = m.update(ReplyCard(id: 1, text: "a\nb", count: 2, lines: [1, 2], at: t0.addingTimeInterval(8)),
                     now: t0.addingTimeInterval(10))
        XCTAssertEqual(m.ago(now: t0.addingTimeInterval(10)), "just now")
        XCTAssertFalse(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(125)))
        XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(131)))
    }

    /// #286: the deck's pages keep their ids while the history grows and drops old replies.
    func testPagesKeepTheirIdsAsTheHistoryMoves() {
        var m = QuickChatModel()
        m.historySecs = 100
        let t0 = Date()
        _ = m.update(card(1, "one"), now: t0)
        let first = m.pageID
        _ = m.update(card(2, "two"), now: t0 + 10)
        XCTAssertEqual(m.pages.map(\.text), ["one", "two"])
        XCTAssertEqual(m.pages.first?.id, first, "the shown card keeps its id once it is kept")
        let second = m.pageID
        XCTAssertEqual(m.pages.last?.id, second)
        _ = m.update(card(3, "three"), now: t0 + 105) // "one" drops off
        XCTAssertEqual(m.pages.map(\.text), ["two", "three"])
        XCTAssertEqual(m.pages.first?.id, second)
    }

    /// #286: every card carries its own header.
    func testEachPageHasItsOwnHeader() {
        var m = QuickChatModel()
        let t0 = Date()
        _ = m.update(ReplyCard(id: 1, text: "a", count: 1, lines: [1], at: t0 - 240), now: t0)
        _ = m.update(ReplyCard(id: 2, text: "b\nc", count: 2, lines: [2, 3], at: t0 - 60), now: t0)
        XCTAssertEqual(m.pages.map { $0.header(now: t0) }, ["Pixie · 4m ago", "2 updates · 1m ago"])
        XCTAssertTrue(m.caption(role: "assistant", text: "Hello there", now: t0))
        XCTAssertEqual(m.pages.last?.header(now: t0), "Pixie", "no time while it is being said")
    }

    func testScrollingToAPageLooksBackOrComesToTheNewest() {
        var m = QuickChatModel()
        for i in UInt64(1)...3 { _ = m.update(card(i, "card \(i)")) }
        let ids = m.pages.map(\.id)
        m.show(page: ids[0])
        XCTAssertEqual(m.shownFull, "card 1")
        XCTAssertEqual(m.position, QuickChatModel.Position(index: 1, count: 3))
        XCTAssertEqual(m.pageID, ids[0])
        m.show(page: ids[2])
        XCTAssertNil(m.viewed)
        XCTAssertEqual(m.pageID, ids[2])
        m.show(page: 999)
        XCTAssertEqual(m.pageID, ids[2], "a page that isn't there changes nothing")
    }

    /// #286: a new card brings the deck to it, even while you look back.
    func testANewCardBringsTheDeckToTheNewest() {
        var m = QuickChatModel()
        _ = m.update(card(1, "one")); _ = m.update(card(2, "two"))
        m.back(now: Date())
        XCTAssertEqual(m.shownFull, "one")
        _ = m.update(card(3, "three"))
        XCTAssertEqual(m.shownFull, "three")
        XCTAssertEqual(m.position, QuickChatModel.Position(index: 3, count: 3))
        XCTAssertFalse(m.newerWaiting)
        XCTAssertEqual(m.pageID, m.pages.last?.id)
    }

    /// #286 tryout: launch as the app runs it. The startup fetch (`agent.replies`, oldest first)
    /// and live `reply.card` events for the same cards arrive in either order, with the settings
    /// read (`captions`, mode) in between: the newest card is on top and is the deck's page.
    func testTheStartupFeedLeavesTheNewestOnTop() {
        let t0 = Date()
        let restored = [
            ReplyCard(id: 1, text: "issues", count: 1, lines: [1], at: t0 - 240),
            ReplyCard(id: 2, text: "bench", count: 1, lines: [2], at: t0 - 180),
            ReplyCard(id: 3, text: "three rows", count: 3, lines: [3, 4, 5], at: t0 - 60),
        ]
        for liveFirst in [false, true] {
            var m = QuickChatModel()
            if liveFirst { for c in restored { _ = m.update(c, now: t0) } }
            m.mode = "text"; m.historySecs = 300; m.prune(now: t0)
            for c in restored { _ = m.update(c, now: t0) }
            if !liveFirst { _ = m.update(restored[2], now: t0) } // a late live copy of the newest
            XCTAssertEqual(m.cardID, 3, "liveFirst \(liveFirst)")
            XCTAssertEqual(m.position, QuickChatModel.Position(index: 3, count: 3))
            XCTAssertEqual(m.pageID, m.pages.last?.id)
            XCTAssertEqual(m.ago(now: t0), "1m ago")
        }
    }

    func testAnUpdateCardIsPutAwayAfterTheDismissTime() {
        var m = QuickChatModel()
        let t0 = Date()
        _ = m.update(card(1, "**a:** done"), now: t0)
        XCTAssertTrue(m.dismissIfOlder(than: 120, now: t0.addingTimeInterval(121)))
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

    // MARK: - buttons to the sessions (#261)

    /// A typed message's line names the sessions it is about; the next message starts with none.
    func test_a_typed_answer_carries_its_sessions() {
        var m = QuickChatModel()
        m.mode = "text"
        m.sent(job: "job_1")
        m.apply(ChatLine(job: "job_1", status: "working", text: "Passed on to fruit-stand.", sessions: ["fruit-claude"]))
        XCTAssertEqual(m.sessions, ["fruit-claude"])
        m.apply(ChatLine(job: "job_1", status: "done", text: "Passed on to both.", sessions: ["fruit-claude", "web-claude"]))
        XCTAssertEqual(m.sessions, ["fruit-claude", "web-claude"])
        m.sending()
        XCTAssertEqual(m.sessions, [])
    }

    /// Pixie's next reply is about the sessions it was just told of; your words and a reply about
    /// nothing have none.
    func test_pixies_reply_carries_the_sessions_it_was_told_of() {
        var m = QuickChatModel()
        m.about(["fruit-claude", "web-claude"])
        XCTAssertEqual(m.sessions, [], "not before Pixie speaks")
        m.saying("Fruit stand")
        XCTAssertEqual(m.sessions, ["fruit-claude", "web-claude"])
        m.saying("Fruit stand is done.")
        XCTAssertEqual(m.sessions, ["fruit-claude", "web-claude"], "the same reply goes on")
        m.caption(role: "assistant", text: "Fruit stand is done.")
        XCTAssertEqual(m.sessions, ["fruit-claude", "web-claude"], "its final words")
        m.heard("thanks")
        XCTAssertEqual(m.sessions, [], "your words")
        m.saying("You're welcome.")
        XCTAssertEqual(m.sessions, [], "a reply about nothing")
        m.caption(role: "assistant", text: "You're welcome.")
        m.about(["api-claude"])
        m.caption(role: "assistant", text: "The API is up.")
        XCTAssertEqual(m.sessions, ["api-claude"], "a reply typed out, not streamed")
    }

    /// Sessions Pixie was told of but never spoke about go with the conversation.
    func test_unspoken_sessions_end_with_the_conversation() {
        var m = QuickChatModel()
        m.about(["fruit-claude"])
        m.conversationEnded()
        m.saying("Hi again.")
        XCTAssertEqual(m.sessions, [])
    }
}
