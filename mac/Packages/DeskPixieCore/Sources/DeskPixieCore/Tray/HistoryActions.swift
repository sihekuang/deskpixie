/// History's `app.action` ids (#461). The core decides what a resume does: it resumes a past
/// session in Herdr, or shows one that is running now.
public enum HistoryActions {
    public static let clearID = "agent:history:clear"

    public static func resumeID(_ sessionID: String) -> String { "agent:history:resume:\(sessionID)" }

    /// Ignored by the core for a running session.
    public static func removeID(_ sessionID: String) -> String { "agent:history:remove:\(sessionID)" }
}

extension HistoryActions {
    /// One row of Settings ▸ Sessions ▸ History: a past session resumes and can be removed; a
    /// running one shows and stays until it ends.
    public struct SettingsRow: Equatable, Sendable, Identifiable {
        /// The session's id.
        public var id: String
        public var label: String
        /// "Resume", or "Show" for a running session.
        public var buttonTitle: String
        public var buttonID: String
        /// False for a session that can't be resumed.
        public var buttonEnabled: Bool
        /// The trash button's action; nil for a running session.
        public var removeID: String?
    }

    public static let emptyText = "No past sessions yet."
    public static let removeHelp = "Remove from History"

    public static func settingsRows(_ refs: [TrayAppState.HistoryRef]) -> [SettingsRow] {
        refs.map { ref in
            SettingsRow(id: ref.id, label: ref.label, buttonTitle: ref.running ? "Show" : "Resume",
                        buttonID: resumeID(ref.id), buttonEnabled: ref.enabled,
                        removeID: ref.running ? nil : removeID(ref.id))
        }
    }

    /// Clear History is offered while anything but running sessions is listed: a running one
    /// stays.
    public static func offersClear(_ refs: [TrayAppState.HistoryRef]) -> Bool {
        refs.contains { !$0.running }
    }
}
