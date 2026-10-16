import Foundation

/// An agent's pane to bring up (the `showPane` event, #110): the args of `agent.focusJob`.
/// An empty worker and job is the coordinator.
public struct PaneRequest: Decodable, Equatable, Sendable {
    public let job: String
    public let worker: String

    public init(job: String, worker: String) {
        self.job = job
        self.worker = worker
    }
}

/// When to bring up a pane an agent is waiting in (#110): only once the user hasn't typed for
/// `typingQuiet`, so a keystroke meant for another app never lands on the agent's approval
/// prompt (Enter or `y` approves in Codex). One request waits at a time; a newer one replaces
/// it, and one still waiting after `maxWait` is dropped (the hint was spoken anyway).
public struct PaneShowGate {
    public static let typingQuiet: TimeInterval = 2
    public static let maxWait: TimeInterval = 120

    private var pending: (request: PaneRequest, since: TimeInterval)?

    public init() {}

    public mutating func request(_ r: PaneRequest, now: TimeInterval) {
        pending = (r, now)
    }

    /// The request to bring up now, if any (it is then forgotten). `sinceLastKey`: seconds since
    /// the user's last key press anywhere.
    public mutating func due(now: TimeInterval, sinceLastKey: TimeInterval) -> PaneRequest? {
        guard let p = pending else { return nil }
        if now - p.since > Self.maxWait {
            pending = nil
            return nil
        }
        guard sinceLastKey >= Self.typingQuiet else { return nil }
        pending = nil
        return p.request
    }
}
