import Foundation

/// The settings the updates bubble and presence follow, read from `settings.snapshot` `values`
/// (settings.json `notifications.{bubble.{enabled,popSecs,clearSecs,lines},awayAfterMins}`; all live).
/// `notifications.debounceSecs` is the core's (restart) and not read here.
public struct BubbleSettingsValues: Equatable, Sendable {
    public var enabled: Bool
    /// Seconds a new update pops before shrinking to the badge (1–60).
    public var popSecs: Int
    /// Seconds the badge stays before it hides (#455; 0–3600, 0 keeps it).
    public var clearSecs: Int
    /// The most lines the bubble lists (1–10).
    public var lines: Int
    /// Minutes without input before the user counts as away (1–60).
    public var awayAfterMins: Int

    public init(enabled: Bool, popSecs: Int, clearSecs: Int = 20, lines: Int, awayAfterMins: Int) {
        self.enabled = enabled
        self.popSecs = popSecs
        self.clearSecs = clearSecs
        self.lines = lines
        self.awayAfterMins = awayAfterMins
    }

    /// Spec §5.3 defaults.
    public static let defaults = BubbleSettingsValues(enabled: true, popSecs: 6, lines: 5, awayAfterMins: 5)

    private struct Snapshot: Decodable {
        struct Bubble: Decodable {
            var enabled: Bool?
            var popSecs: Int?
            var clearSecs: Int?
            var lines: Int?

            private enum CodingKeys: String, CodingKey { case enabled, popSecs, clearSecs, lines }

            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                enabled = try? c.decodeIfPresent(Bool.self, forKey: .enabled)
                popSecs = try? c.decodeIfPresent(Int.self, forKey: .popSecs)
                clearSecs = try? c.decodeIfPresent(Int.self, forKey: .clearSecs)
                lines = try? c.decodeIfPresent(Int.self, forKey: .lines)
            }
        }

        struct Notifications: Decodable {
            var bubble: Bubble?
            var awayAfterMins: Int?

            private enum CodingKeys: String, CodingKey { case bubble, awayAfterMins }

            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                bubble = try? c.decodeIfPresent(Bubble.self, forKey: .bubble)
                awayAfterMins = try? c.decodeIfPresent(Int.self, forKey: .awayAfterMins)
            }
        }

        struct Values: Decodable {
            var notifications: Notifications?

            private enum CodingKeys: String, CodingKey { case notifications }

            /// Lenient: a malformed field falls back to its default instead of failing the rest.
            init(from decoder: Decoder) throws {
                let c = try decoder.container(keyedBy: CodingKeys.self)
                notifications = try? c.decodeIfPresent(Notifications.self, forKey: .notifications)
            }
        }

        var values: Values
    }

    /// nil when the JSON is not a snapshot. Values outside the schema's ranges fall back to the
    /// defaults (the core validates writes; this guards hand-edited settings.json).
    public static func fromSnapshot(_ data: Data) -> BubbleSettingsValues? {
        guard let v = try? JSONDecoder().decode(Snapshot.self, from: data).values else { return nil }
        var s = defaults
        let n = v.notifications
        if let e = n?.bubble?.enabled { s.enabled = e }
        if let p = n?.bubble?.popSecs, (1...60).contains(p) { s.popSecs = p }
        if let c = n?.bubble?.clearSecs, (0...3600).contains(c) { s.clearSecs = c }
        if let l = n?.bubble?.lines, (1...10).contains(l) { s.lines = l }
        if let a = n?.awayAfterMins, (1...60).contains(a) { s.awayAfterMins = a }
        return s
    }
}
