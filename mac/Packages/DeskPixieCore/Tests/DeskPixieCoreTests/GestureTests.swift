import CoreGraphics
import Foundation
import XCTest
@testable import DeskPixieCore

/// Port of the gesture cases in ai-mascot ui/mascot/test/player-core.test.js.
/// JS times are milliseconds; Gesture takes seconds, so `ms` converts.
final class GestureTests: XCTestCase {
    private func ms(_ t: Double) -> TimeInterval { t / 1000 }
    private func pt(_ x: CGFloat = 10, _ y: CGFloat = 10) -> CGPoint { CGPoint(x: x, y: y) }

    private func down(_ g: inout Gesture, _ t: Double, _ x: CGFloat = 10, _ y: CGFloat = 10, left: Bool = true) -> GestureResult {
        g.down(at: pt(x, y), time: ms(t), leftButton: left)
    }

    private func up(_ g: inout Gesture, _ t: Double, _ x: CGFloat = 10, _ y: CGFloat = 10, left: Bool = true) -> GestureResult {
        g.up(at: pt(x, y), time: ms(t), leftButton: left)
    }

    func testAClickInPlaceWaits300msThenIsAClick() {
        var g = Gesture()
        XCTAssertEqual(down(&g, 0), .press)
        XCTAssertEqual(up(&g, 80, 11, 9), .wait)
        XCTAssertEqual(g.expire(now: ms(380)), GestureResult.none, "a second click may still come")
        XCTAssertEqual(g.expire(now: ms(381)), .click)
        XCTAssertEqual(g.expire(now: ms(900)), GestureResult.none, "only once")
    }

    func testASecondClickWithin300msIsADoubleNeverAClick() {
        var g = Gesture()
        _ = down(&g, 0)
        _ = up(&g, 80)
        XCTAssertEqual(down(&g, 250), .press)
        XCTAssertEqual(g.expire(now: ms(381)), GestureResult.none, "the second press is held past 300 ms")
        XCTAssertEqual(up(&g, 400), .double)
        XCTAssertEqual(g.expire(now: ms(1000)), GestureResult.none)
    }

    func testMovingMoreThan3pxIsADragWithNoClick() {
        var g = Gesture()
        _ = down(&g, 0)
        XCTAssertEqual(g.move(to: pt(12, 12)), GestureResult.none, "within slop")
        XCTAssertEqual(g.move(to: pt(14, 10)), .drag)
        XCTAssertEqual(g.move(to: pt(40, 10)), GestureResult.none, "drag starts once")
        XCTAssertEqual(up(&g, 40, 40, 10), GestureResult.none)
        XCTAssertEqual(g.expire(now: ms(1000)), GestureResult.none)
    }

    func testADragOnTheSecondPressCancelsThePendingClick() {
        var g = Gesture()
        _ = down(&g, 0)
        _ = up(&g, 50)
        _ = down(&g, 150)
        XCTAssertEqual(g.move(to: pt(30, 10)), .drag)
        XCTAssertEqual(g.expire(now: ms(1000)), GestureResult.none)
    }

    func testASlowSecondClickIsTwoSingleClicks() {
        var g = Gesture()
        _ = down(&g, 0)
        _ = up(&g, 50)
        XCTAssertEqual(g.expire(now: ms(351)), .click)
        _ = down(&g, 400)
        XCTAssertEqual(up(&g, 450), .wait)
        XCTAssertEqual(g.expire(now: ms(751)), .click)
    }

    func testRightClicksAndStrayReleasesDoNothing() {
        var g = Gesture()
        XCTAssertEqual(down(&g, 0, left: false), GestureResult.none)
        XCTAssertEqual(up(&g, 10, left: false), GestureResult.none)
        XCTAssertEqual(up(&g, 20), GestureResult.none, "release without press")
        XCTAssertEqual(g.expire(now: ms(1000)), GestureResult.none)
    }

    func testAFastTripleClickIsOneDoubleAndNoClick() {
        var g = Gesture()
        var seen: [GestureResult] = []
        _ = down(&g, 0); seen.append(up(&g, 50))
        _ = down(&g, 150); seen.append(up(&g, 200))
        XCTAssertEqual(down(&g, 300), GestureResult.none, "third press is swallowed")
        seen.append(up(&g, 350))
        seen.append(g.expire(now: ms(1000)))
        seen.append(g.expire(now: ms(5000)))
        XCTAssertEqual(seen.filter { $0 == .double || $0 == .click }, [.double])
    }

    func testExactly300msIsWithinTheWindowInDownAndExpireAlike() {
        var g = Gesture()
        _ = down(&g, 0)
        _ = up(&g, 100)
        XCTAssertEqual(g.expire(now: ms(400)), GestureResult.none)
        XCTAssertEqual(down(&g, 400), .press)
        XCTAssertEqual(up(&g, 420), .double)
    }
}
