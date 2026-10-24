import XCTest
@testable import DeskPixieCore

/// Spec §4 lifecycle of the updates bubble beside Pixie: hidden / popped / badge / expanded, the
/// 1 s "seen" rule, away → badge only → one pop on return. Times are seconds on one clock.
final class UpdatesBubbleModelTests: XCTestCase {
    private func line(_ id: UInt64) -> UpdateLine {
        UpdateLine(id: id, text: "line \(id)", kind: "reply", at: Int64(id), job: nil, worker: nil)
    }

    /// #286: the badge deck shows one card at a time, oldest first; +1 is newer.
    func testTheBadgeDeckShowsOneAtATimeAndClampsWhenOneIsAnswered() {
        var m = UpdatesBubbleModel()
        m.apply([line(1), line(2), line(3)], now: 0, pop: .allowed)
        XCTAssertEqual(m.current?.id, 1)
        m.step(1); m.step(1); m.step(1)
        XCTAssertEqual(m.current?.id, 3)
        m.apply([line(1), line(2)], now: 1, pop: .allowed) // 3 was answered
        XCTAssertEqual(m.current?.id, 2)
        XCTAssertEqual(m.deckPosition?.index, 2)
    }

    /// #286: a card you act on leaves at once, the next slides in, and the core's list that
    /// still has it doesn't bring it back; once the core drops it, the id is forgotten.
    func testACardYouActOnLeavesAtOnceAndStaysGone() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let lines = [question(1, "hawk"), question(2, "lynx"), question(3, "owl")]
        m.apply(lines, now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        m.step(1)
        XCTAssertEqual(m.current?.id, 2)
        m.act(on: 2)
        XCTAssertEqual(m.count, 2)
        XCTAssertEqual(m.current?.id, 3, "the next slides into its place")
        m.apply(lines, now: 3, pop: .allowed)              // the core hasn't caught up
        XCTAssertEqual(m.lines.map(\.id), [1, 3])
        m.act(on: 3)
        XCTAssertEqual(m.current?.id, 1, "the last one gone: the one before")
        m.act(on: 1)
        XCTAssertEqual(m.phase, .hidden)
        m.apply([question(1, "hawk"), question(3, "owl")], now: 4, pop: .allowed) // core dropped 2
        m.apply([question(1, "hawk"), question(2, "lynx2"), question(3, "owl")], now: 5, pop: .allowed)
        XCTAssertEqual(m.lines.map(\.id), [2], "2 was forgotten once dropped; 1 and 3 are still acted on")
    }

    /// #286: a scroll to a card moves the deck there; an id no longer listed changes nothing.
    func testAScrollToACardMovesTheDeck() {
        var m = UpdatesBubbleModel()
        m.apply([line(1), line(2), line(3)], now: 0, pop: .allowed)
        m.show(id: 3)
        XCTAssertEqual(m.current?.id, 3)
        XCTAssertEqual(m.deckPosition?.index, 3)
        m.show(id: 9)
        XCTAssertEqual(m.current?.id, 3)
    }

    func testTheDeckKeepsItsCardWhenOthersArriveAndSaysOneOfOneAlone() {
        var m = UpdatesBubbleModel()
        m.apply([line(1), line(2)], now: 0, pop: .allowed)
        m.step(1)
        XCTAssertEqual(m.current?.id, 2)
        m.apply([line(1), line(2), line(3)], now: 1, pop: .allowed)
        XCTAssertEqual(m.current?.id, 2)
        m.step(-1); m.step(-1)
        XCTAssertEqual(m.current?.id, 1, "stops at the oldest")
        m.apply([line(1)], now: 2, pop: .allowed)
        XCTAssertEqual(m.deckPosition?.index, 1)
        XCTAssertEqual(m.deckPosition?.count, 1)
        m.apply([], now: 3, pop: .allowed)
        XCTAssertNil(m.deckPosition)
        XCTAssertNil(m.current)
    }

    func testDismissingTheCurrentCardClampsTheDeck() {
        var m = UpdatesBubbleModel()
        m.apply([line(1), line(2)], now: 0, pop: .allowed)
        m.step(1)
        XCTAssertTrue(m.dismiss(2))
        XCTAssertEqual(m.current?.id, 1)
        XCTAssertEqual(m.deckIndex, 0)
    }

    /// #105: a session's menu offers its options instead of Allow and Deny.
    /// #105: a session's dialog carries its command, shown small under the line.
    /// #105: a question waiting on your answer keeps the bubble open; it collapses once answered.
    func testAQuestionKeepsTheBubbleOpenUntilAnswered() {
        var m = UpdatesBubbleModel(popSecs: 6); m.apply([], now: 0, pop: .allowed)
        let ask = UpdateLine(id: 1, text: "api: Do you want to proceed?", kind: "question", at: 1, job: nil, worker: nil,
                             token: "hawk", options: ["Yes", "No"])
        m.apply([ask], now: 10, pop: .allowed)
        m.tick(now: 60)
        XCTAssertEqual(m.phase, .popped(until: 16), "still open a minute later")
        m.apply([], now: 61, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testAnUpdateWithoutButtonsStillCollapses() {
        var m = UpdatesBubbleModel(popSecs: 6); m.apply([], now: 0, pop: .allowed)
        m.apply([UpdateLine(id: 1, text: "done", kind: "reply", at: 1, job: nil, worker: nil)], now: 10, pop: .allowed)
        m.tick(now: 17)
        XCTAssertEqual(m.phase, .badge)
    }

    func testADialogLineCarriesItsCommand() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":8,"text":"api: Run the tests. Do you want to proceed? Choose in the bubble.","kind":"question","at":8,"token":"owl","options":["Yes","No"],"note":"npm test"}"#.utf8))
        XCTAssertEqual(line.note, "npm test")
        XCTAssertTrue(line.offersOptions)
    }

    func testASessionMenuOffersItsOptions() throws {
        let menu = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":6,"text":"deskpixie is asking: Push main?","kind":"question","at":6,"token":"hawk","options":["Push","Don't push"]}"#.utf8))
        XCTAssertEqual(menu.options, ["Push", "Don't push"])
        XCTAssertTrue(menu.offersOptions)
        XCTAssertFalse(menu.offersDecision)
        let permission = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":7,"text":"Run npm test in api? Say confirm owl.","kind":"question","at":7,"token":"owl"}"#.utf8))
        XCTAssertTrue(permission.offersDecision)
        XCTAssertFalse(permission.offersOptions)
    }


    func testASessionLineCarriesItsApp() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":4,"text":"api is done: tests pass.","kind":"session","at":4,"app":"com.jetbrains.WebStorm"}"#.utf8))
        XCTAssertEqual(line.app, "com.jetbrains.WebStorm")
        XCTAssertFalse(line.offersDecision)
        let reply = try JSONDecoder().decode(UpdateLine.self, from: Data(#"{"id":5,"text":"Done.","kind":"reply","at":5}"#.utf8))
        XCTAssertNil(reply.app)
    }

    func testASessionLineCarriesItsSession() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":4,"text":"api is done.","kind":"session","at":4,"app":"dev.warp.Warp-Stable","session":"api-claude"}"#.utf8))
        XCTAssertEqual(line.session, "api-claude")
    }

    func testAgeIsShort() {
        XCTAssertEqual(updateAge(seconds: 5), "now")
        XCTAssertEqual(updateAge(seconds: 63), "1m ago")
        XCTAssertEqual(updateAge(seconds: 7_300), "2h ago")
        XCTAssertEqual(updateAge(seconds: 200_000), "2d ago")
    }

    /// #197: the bubble shows the brief under its title; a line without one shows its text.
    func testALineShowsItsBriefWhenItHasOne() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":4,"text":"api: fix login is done: tests pass.","kind":"session","at":4,"title":"api: fix login","brief":"Tests pass.","asking":true}"#.utf8))
        XCTAssertEqual(line.title, "api: fix login")
        XCTAssertEqual(line.shownText, "Tests pass.")
        XCTAssertEqual(line.asking, true)
        let reply = try JSONDecoder().decode(UpdateLine.self, from: Data(#"{"id":5,"text":"Done.","kind":"reply","at":5}"#.utf8))
        XCTAssertEqual(reply.shownText, "Done.")
        XCTAssertNil(reply.title)
    }

    /// #194: a clicked line leaves the open bubble at once; the rest stay.
    func testAClickedLineIsDismissed() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        XCTAssertTrue(m.dismiss(2))
        XCTAssertEqual(m.shown.map(\.id), [1])
        XCTAssertEqual(m.count, 1)
        m.tick(now: 3)
        XCTAssertEqual(m.shown.map(\.id), [1], "the rest stay")
    }

    func testDismissingTheLastLineHidesTheBubble() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        XCTAssertTrue(m.dismiss(1))
        XCTAssertEqual(m.phase, .hidden)
    }

    /// The core's list may still carry it before it handles the seen call: it doesn't come back
    /// or pop again.
    func testADismissedLineDoesNotComeBack() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.tick(now: 8)
        XCTAssertTrue(m.dismiss(2))
        m.apply([line(1), line(2)], now: 9, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)
        XCTAssertEqual(m.count, 1)
    }

    /// #199: a job's question (no buttons) is clicked away like any line.
    func testAJobsQuestionIsDismissedByAClick() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let ask = UpdateLine(id: 1, text: "Which branch?", kind: "question", at: 1, job: "job_1", worker: nil)
        m.apply([ask, line(2)], now: 1, pop: .allowed)
        XCTAssertTrue(m.dismiss(1))
        XCTAssertEqual(m.count, 1)
    }

    /// #199 review: an old notification whose id now names another line is stale.
    func testAnOldNotificationsLineIsStale() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(5)], now: 1, pop: .allowed)
        XCTAssertFalse(m.isStale(line(5)))
        XCTAssertTrue(m.isStale(UpdateLine(id: 5, text: "older", kind: "reply", at: 1, job: nil, worker: nil)))
        XCTAssertFalse(m.isStale(line(9)), "a line no longer listed: dismissing it is harmless")
    }

    func testAQuestionWithButtonsIsNotDismissedByAClick() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 1, pop: .allowed)
        XCTAssertFalse(m.dismiss(1))
        XCTAssertEqual(m.count, 1)
    }

    func testAPendingQuestionOffersAllowAndDeny() throws {
        let decode = { (json: String) throws -> UpdateLine in
            try JSONDecoder().decode(UpdateLine.self, from: Data(json.utf8))
        }
        let line = try decode(#"{"id":1,"text":"Run git push in focus? Say confirm hawk.","kind":"question","at":1,"token":"hawk"}"#)
        XCTAssertEqual(line.token, "hawk")
        XCTAssertTrue(line.offersDecision)
        let info = try decode(#"{"id":2,"text":"Done.","kind":"reply","at":2}"#)
        XCTAssertFalse(info.offersDecision)
        let jobQuestion = try decode(#"{"id":3,"text":"Which one?","kind":"question","at":3}"#)
        XCTAssertFalse(jobQuestion.offersDecision)
    }

    private func question(_ id: UInt64, _ token: String) -> UpdateLine {
        UpdateLine(id: id, text: "Run it? Say confirm \(token).", kind: "question", at: Int64(id), job: nil,
                   worker: nil, token: token)
    }

    /// #97: a click landing as the buttons appear never approves: Allow and Deny ignore taps for
    /// 0.75 s after that question's buttons appear.
    func testAllowAndDenyIgnoreTapsJustAfterTheButtonsAppear() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)       // pops: buttons appear at 10
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8))
    }

    /// Review of #105: a session menu's option buttons are guarded the same way, then count.
    func testMenuOptionsAcceptTapsOnceShownLongEnough() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let menu = UpdateLine(id: 1, text: "Push main?", kind: "question", at: 1, job: nil, worker: nil,
                              token: "hawk", options: ["Push", "Don't push"])
        m.apply([menu], now: 10, pop: .allowed)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8))
    }

    /// #286: the deck's card can be older than the newest maxLines; its buttons still count.
    func testTheDecksCardIsGuardedThenAcceptedWhateverItsPlace() {
        var m = UpdatesBubbleModel(maxLines: 2); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk"), line(2), line(3)], now: 10, pop: .allowed)
        XCTAssertEqual(m.current?.id, 1)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.2))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8))
    }

    /// #286: the badge card's header names the kind and who it is about.
    func testTheDeckHeaderNamesTheKindAndSource() {
        var perm = question(1, "hawk")
        XCTAssertEqual(perm.deckHeader, "Permission")
        perm = UpdateLine(id: 1, text: "Run it?", kind: "question", at: 1, job: nil, worker: nil,
                          token: "hawk", title: "coordinator")
        XCTAssertEqual(perm.deckHeader, "Permission · coordinator")
        XCTAssertFalse(perm.offersTerminal)
        let asks = UpdateLine(id: 2, text: "Resize the VM?", kind: "session", at: 2, job: nil, worker: nil,
                              session: "trader", title: "trader", asking: true)
        XCTAssertEqual(asks.deckHeader, "trader is asking")
        XCTAssertTrue(asks.offersTerminal)
        let menu = UpdateLine(id: 3, text: "Push?", kind: "question", at: 3, job: nil, worker: nil,
                              token: "owl", title: "deskpixie", options: ["Push", "Don't"])
        XCTAssertEqual(menu.deckHeader, "deskpixie is asking")
        XCTAssertFalse(menu.offersTerminal)
        let jobQuestion = UpdateLine(id: 4, text: "Which branch?", kind: "question", at: 4, job: "job_1", worker: nil)
        XCTAssertEqual(jobQuestion.deckHeader, "Question")
        XCTAssertTrue(jobQuestion.offersTerminal)
        XCTAssertEqual(line(5).deckHeader, "Update")
        XCTAssertFalse(line(5).offersTerminal)
    }

    func testTheGuardIsPerQuestion() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        m.apply([question(1, "hawk"), question(2, "lynx")], now: 11, pop: .allowed)
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 11.2))
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 11.2), "not on screen: the deck shows hawk")
        m.step(1)                                                    // lynx's buttons appear at 12
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 12))
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 12.5))
        XCTAssertTrue(m.acceptsDecision(token: "lynx", now: 12.8))
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 12.8), "stepped away from")
    }

    /// #286 re-review: a card brought up by a step, a scroll or acting on the one before is
    /// stamped then, so a tap after the guard counts with no tick in between.
    func testACardBroughtUpIsStampedWhenItComesUp() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk"), question(2, "lynx"), question(3, "owl")], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        m.step(1, now: 20)
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 20.1))
        XCTAssertTrue(m.acceptsDecision(token: "lynx", now: 20 + UpdatesBubbleModel.decisionGuard))
        m.act(on: 2, now: 30)
        XCTAssertEqual(m.current?.id, 3)
        XCTAssertFalse(m.acceptsDecision(token: "owl", now: 30.1))
        XCTAssertTrue(m.acceptsDecision(token: "owl", now: 30 + UpdatesBubbleModel.decisionGuard))
        m.show(id: 1, now: 40)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 40.1))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 40 + UpdatesBubbleModel.decisionGuard))
    }

    /// #286: while you're around a pop holds, but its buttons are still stamped.
    func testAHeldPopStaysAndStillStamps() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        m.tick(now: 100, holding: true)
        XCTAssertEqual(m.phase, .popped(until: 1 + m.popSecs))
        m.tick(now: 101)
        XCTAssertEqual(m.phase, .badge)
    }

    /// #286 review: once a card is answered the next slides up under the pointer; a second tap
    /// meant for the first never answers it.
    func testTheCardThatSlidesUpIsGuardedAgain() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk"), question(2, "lynx")], now: 10, pop: .allowed)
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 11))
        m.apply([question(2, "lynx")], now: 11.1, pop: .allowed)     // hawk answered
        XCTAssertEqual(m.current?.id, 2)
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 11.2))
        XCTAssertFalse(m.acceptsDecision(token: "lynx", now: 11.8))
        XCTAssertTrue(m.acceptsDecision(token: "lynx", now: 11.1 + UpdatesBubbleModel.decisionGuard))
    }

    /// #268: buttons you open the bubble onto (hovering its badge) take your tap at once once you
    /// moved to them: a quick move and click was dropped without a sign. A tap where the pointer
    /// was when it opened (a click meant for the badge, or for what is under it) stays guarded.
    func testButtonsYouHoverOpenAndMoveToTakeATapAtOnce() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        m.pointerEntered(now: 12); m.pointerLeft()                 // hovered and left: the badge
        m.tick(now: 17)
        XCTAssertEqual(m.phase, .badge)
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 17.5)) // no buttons on a badge
        m.pointerEntered(now: 20, at: CGPoint(x: 300, y: 40))     // over the badge
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 20.2, at: CGPoint(x: 120, y: 90)))
    }

    func testATapWhereTheBubbleOpenedIsStillGuarded() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        m.pointerEntered(now: 12); m.pointerLeft()
        m.tick(now: 17)
        m.pointerEntered(now: 20, at: CGPoint(x: 300, y: 40))
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 20.2, at: CGPoint(x: 301, y: 40)))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 20.8, at: CGPoint(x: 301, y: 40)))
    }

    /// The pop's guard holds over a hover right after it (a pop under a resting pointer, #97).
    func testAPopsGuardSurvivesAHover() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        m.pointerEntered(now: 10.1, at: CGPoint(x: 300, y: 40))
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 10.3, at: CGPoint(x: 120, y: 90)))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 10.8, at: CGPoint(x: 120, y: 90)))
    }

    func testAQuestionArrivingWhileOpenIsStillGuarded() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 10, pop: .allowed)
        m.pointerEntered(now: 11, at: CGPoint(x: 300, y: 40))
        m.apply([line(1), question(2, "hawk")], now: 12, pop: .allowed)
        m.step(1); m.tick(now: 12)                                       // its card comes up
        XCTAssertFalse(m.acceptsDecision(token: "hawk", now: 12.2, at: CGPoint(x: 120, y: 90)))
        XCTAssertTrue(m.acceptsDecision(token: "hawk", now: 12.8))
    }

    func testATapOnAQuestionNotShownIsIgnored() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([question(1, "hawk")], now: 10, pop: .allowed)
        XCTAssertFalse(m.acceptsDecision(token: "owl", now: 12))
    }

    func testFirstListAfterLaunchIsBadgeOnly() {
        var m = UpdatesBubbleModel()
        m.apply([line(1)], now: 0, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)
    }

    func testFirstEmptyListAfterLaunchIsHidden() {
        var m = UpdatesBubbleModel()
        XCTAssertEqual(m.phase, .hidden)
        m.apply([], now: 0, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testNewLinePopsThenCollapsesToBadge() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 10, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 16))
        m.tick(now: 15.9); XCTAssertEqual(m.phase, .popped(until: 16))
        m.tick(now: 16); XCTAssertEqual(m.phase, .badge)
    }

    func testAnotherLineWhilePoppedRestartsTheTimer() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 10, pop: .allowed)
        m.apply([line(1), line(2)], now: 14, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 20))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
    }

    func testSameListAgainDoesNotPop() {
        var m = UpdatesBubbleModel(); m.apply([line(1)], now: 0, pop: .allowed)
        m.apply([line(1)], now: 5, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)
    }

    /// #198: looking marks nothing seen: the lines are all still there after the pointer leaves.
    func testHoverExpandsAndMarksNothingSeen() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        XCTAssertEqual(m.phase, .expanded(since: 2))
        m.tick(now: 10)
        m.pointerLeft()
        XCTAssertEqual(m.phase, .badge)
        XCTAssertEqual(m.count, 2)
    }

    /// #198: a click dismisses only its line; after the pointer leaves the others remain.
    func testAClickDismissesOnlyItsLine() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2), line(3)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        XCTAssertTrue(m.dismiss(2))
        m.tick(now: 5)
        m.pointerLeft()
        XCTAssertEqual(m.count, 2)
        m.pointerEntered(now: 6)
        XCTAssertEqual(m.shown.map(\.id), [3, 1])
    }

    func testPointerOverAnEmptyHiddenBubbleDoesNothing() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.pointerEntered(now: 1)
        XCTAssertEqual(m.phase, .hidden)
        m.tick(now: 3)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testExpandedListStaysUntilThePointerLeaves() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2); m.tick(now: 3)
        m.apply([], now: 3.1, pop: .allowed)            // heard in a conversation meanwhile
        XCTAssertEqual(m.phase, .expanded(since: 2))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
        m.pointerLeft()
        XCTAssertEqual(m.phase, .hidden)
    }

    func testALineArrivingWhileExpandedIsAddedOnTopAndStaysOpen() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2)
        m.apply([line(1), line(2)], now: 2.5, pop: .allowed)
        XCTAssertEqual(m.phase, .expanded(since: 2))
        XCTAssertEqual(m.shown.map(\.id), [2, 1])
    }

    func testPendingQuestionKeepsTheBadgeAfterLeaving() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1), line(2)], now: 1, pop: .allowed)
        m.pointerEntered(now: 2); m.tick(now: 3)
        m.apply([line(2)], now: 3.1, pop: .allowed)
        m.pointerLeft()
        XCTAssertEqual(m.phase, .badge); XCTAssertEqual(m.count, 1)
        XCTAssertEqual(m.shown.map(\.id), [2])
    }

    func testAwayHoldsThePopAndReturnPopsOnce() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .hold)
        m.apply([line(1), line(2)], now: 2, pop: .hold)
        XCTAssertEqual(m.phase, .badge)
        m.back(now: 100, pop: .allowed)
        XCTAssertEqual(m.phase, .popped(until: 106))
        m.tick(now: 106)
        m.back(now: 200, pop: .allowed)
        XCTAssertEqual(m.phase, .badge)                 // nothing new: no second pop
    }

    func testReturnWithNothingLeftDoesNotPop() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .hold)
        m.apply([], now: 2, pop: .hold)                 // heard elsewhere while away
        m.back(now: 100, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
    }

    func testBadgeOnlyNeverPops() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([line(1)], now: 1, pop: .badgeOnly)
        XCTAssertEqual(m.phase, .badge)
    }

    func testEmptyListHides() {
        var m = UpdatesBubbleModel(); m.apply([line(1)], now: 0, pop: .allowed)
        m.apply([], now: 1, pop: .allowed)
        XCTAssertEqual(m.phase, .hidden)
        XCTAssertEqual(m.count, 0)
    }

    func testMoreCountsBeyondMaxLines() {
        var m = UpdatesBubbleModel(popSecs: 6, maxLines: 2)
        m.apply((1...5).map(line), now: 0, pop: .allowed)
        XCTAssertEqual(m.shown.map(\.id), [5, 4]); XCTAssertEqual(m.more, 3)
        XCTAssertEqual(m.count, 5)
    }

    // M2: a line added while open, after the first seen call, is reported once shown 1 s.
    // M1: hover counts only while the bubble can be seen.
    func testHoverCountsOnlyWhenTheBubbleIsVisible() {
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0, alreadyInside: false))
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0.05, alreadyInside: false))
        XCTAssertTrue(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0.5, alreadyInside: false))
        XCTAssertTrue(UpdatesBubbleModel.hoverCounts(pointerOnPanel: true, alpha: 0, alreadyInside: true),
                      "already open under the pointer: it is drawn at full opacity")
        XCTAssertFalse(UpdatesBubbleModel.hoverCounts(pointerOnPanel: false, alpha: 1, alreadyInside: true))
    }

    // MARK: typed answers (#268)

    private func typedMenu(_ id: UInt64 = 1, _ token: String = "hawk") -> UpdateLine {
        UpdateLine(id: id, text: "Which database?", kind: "question", at: Int64(id), job: nil, worker: nil,
                   token: token, options: ["Postgres", "SQLite"], details: ["Relational", ""], typed: true)
    }

    /// A menu held in its session's hook offers a typed answer and "Chat about this"; its
    /// options carry their descriptions.
    func testAHeldMenuOffersATypedAnswer() throws {
        let line = try JSONDecoder().decode(UpdateLine.self, from: Data(
            #"{"id":9,"text":"proj is asking: Which database?","kind":"question","at":9,"token":"hawk","options":["Postgres","SQLite"],"details":["Relational",""],"typed":true}"#.utf8))
        XCTAssertTrue(line.offersTyped)
        XCTAssertEqual(line.details, ["Relational", ""])
        XCTAssertEqual(line.detail(of: 0), "Relational")
        XCTAssertNil(line.detail(of: 1), "an empty description shows nothing")
        XCTAssertNil(line.detail(of: 5))
        let keys = UpdateLine(id: 1, text: "Push main?", kind: "question", at: 1, job: nil, worker: nil,
                              token: "hawk", options: ["Push", "Don't push"])
        XCTAssertFalse(keys.offersTyped, "a menu answered by keys takes no typed answer")
    }

    /// While you type the answer the bubble stays open, even when the pointer leaves it.
    func testTypingKeepsTheBubbleOpen() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([typedMenu()], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        XCTAssertTrue(m.startTyping(token: "hawk"))
        XCTAssertEqual(m.typing, "hawk")
        m.pointerLeft()
        XCTAssertEqual(m.phase, .expanded(since: 11))
        m.tick(now: 100)
        XCTAssertEqual(m.phase, .expanded(since: 11))
        m.stopTyping()
        XCTAssertNil(m.typing)
        XCTAssertEqual(m.phase, .badge, "put away with the pointer gone: back to the badge")
    }

    /// Only a menu that takes a typed answer can be typed into.
    func testOnlyATypedMenuCanBeTypedInto() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let keys = UpdateLine(id: 2, text: "Push main?", kind: "question", at: 2, job: nil, worker: nil,
                              token: "owl", options: ["Push", "Don't push"])
        m.apply([keys], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        XCTAssertFalse(m.startTyping(token: "owl"))
        XCTAssertFalse(m.startTyping(token: "nope"))
        XCTAssertNil(m.typing)
    }

    /// The question stops waiting (answered in its terminal, or its wait ran out): typing ends.
    func testTypingEndsWhenTheQuestionGoes() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        m.apply([typedMenu()], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        XCTAssertTrue(m.startTyping(token: "hawk"))
        m.apply([], now: 12, pop: .allowed)
        XCTAssertNil(m.typing)
    }

    /// Review of #268: the pointer left while you typed, then the question went (its wait ran
    /// out, or it was answered in the terminal): the bubble closes, as the pointer leaving would.
    func testTheBubbleClosesWhenItsQuestionGoesAfterThePointerLeft() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let other = UpdateLine(id: 5, text: "Done.", kind: "reply", at: 5, job: nil, worker: nil)
        m.apply([other, typedMenu()], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        XCTAssertTrue(m.startTyping(token: "hawk"))
        m.pointerLeft()
        m.apply([other], now: 12, pop: .allowed)
        XCTAssertNil(m.typing)
        XCTAssertEqual(m.phase, .badge)
    }

    /// The pointer is still over it: the bubble stays open when the question goes.
    func testTheBubbleStaysOpenWhenItsQuestionGoesUnderThePointer() {
        var m = UpdatesBubbleModel(); m.apply([], now: 0, pop: .allowed)
        let other = UpdateLine(id: 5, text: "Done.", kind: "reply", at: 5, job: nil, worker: nil)
        m.apply([other, typedMenu()], now: 10, pop: .allowed)
        m.pointerEntered(now: 11)
        XCTAssertTrue(m.startTyping(token: "hawk"))
        m.apply([other], now: 12, pop: .allowed)
        XCTAssertEqual(m.phase, .expanded(since: 11))
    }
}
