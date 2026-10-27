import Foundation

/// The options ticked of a menu question that takes several (#364), by number from 1: a tap
/// ticks one or unticks it; Send goes once one is ticked, with them in order.
public struct MenuPicks: Equatable, Sendable {
    public private(set) var picked: Set<Int> = []

    public init() {}

    public mutating func toggle(_ option: Int) {
        if picked.contains(option) { picked.remove(option) } else { picked.insert(option) }
    }

    public func isPicked(_ option: Int) -> Bool { picked.contains(option) }

    public var canSend: Bool { !picked.isEmpty }

    /// What `agent.pick` takes: the options ticked, in order.
    public var options: [Int] { picked.sorted() }
}
