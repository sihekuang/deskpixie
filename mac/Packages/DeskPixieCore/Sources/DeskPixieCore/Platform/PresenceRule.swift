import Foundation

/// Spec §4 "Away": the screen is locked, the displays are asleep, or there has been no
/// keyboard/mouse input for `notifications.awayAfterMins`. While away nothing pops and Pixie
/// doesn't start a conversation.
public enum PresenceRule {
    public static func isAway(locked: Bool, displaysAsleep: Bool, idleSecs: TimeInterval, awayAfterMins: Int) -> Bool {
        locked || displaysAsleep || idleSecs >= TimeInterval(awayAfterMins) * 60
    }
}
