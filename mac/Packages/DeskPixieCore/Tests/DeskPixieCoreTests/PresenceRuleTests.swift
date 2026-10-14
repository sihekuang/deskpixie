import XCTest
@testable import DeskPixieCore

/// Spec §4 "Away": screen locked, displays asleep, or no input for `awayAfterMins`.
final class PresenceRuleTests: XCTestCase {
    func testLockedIsAway() {
        XCTAssertTrue(PresenceRule.isAway(locked: true, displaysAsleep: false, idleSecs: 0, awayAfterMins: 5))
    }

    func testDisplaysAsleepIsAway() {
        XCTAssertTrue(PresenceRule.isAway(locked: false, displaysAsleep: true, idleSecs: 0, awayAfterMins: 5))
    }

    func testIdleUpToTheLimitIsPresent() {
        XCTAssertFalse(PresenceRule.isAway(locked: false, displaysAsleep: false, idleSecs: 299, awayAfterMins: 5))
        XCTAssertTrue(PresenceRule.isAway(locked: false, displaysAsleep: false, idleSecs: 300, awayAfterMins: 5))
        XCTAssertFalse(PresenceRule.isAway(locked: false, displaysAsleep: false, idleSecs: 300, awayAfterMins: 6))
    }
}
