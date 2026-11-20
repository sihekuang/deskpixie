/// History's `app.action` ids (#461). The core decides what a resume does: it resumes a past
/// session in Herdr, or shows one that is running now.
public enum HistoryActions {
    public static let clearID = "agent:history:clear"

    public static func resumeID(_ sessionID: String) -> String { "agent:history:resume:\(sessionID)" }

    /// Ignored by the core for a running session.
    public static func removeID(_ sessionID: String) -> String { "agent:history:remove:\(sessionID)" }
}
