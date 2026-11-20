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

    /// Said for an empty History, in the tray and in Settings.
    public static let emptyText = "No past sessions"
    public static let removeHelp = "Remove from History"

    public static func settingsRows(_ refs: [TrayAppState.HistoryRef]) -> [SettingsRow] {
        refs.map { ref in
            SettingsRow(id: ref.id, label: ref.label, buttonTitle: ref.running ? "Show" : "Resume",
                        buttonID: resumeID(ref.id), buttonEnabled: ref.enabled,
                        removeID: ref.running ? nil : removeID(ref.id))
        }
    }

    /// Settings ▸ Sessions ▸ History, worked out once per draw.
    public struct SettingsList: Equatable, Sendable {
        public var rows: [SettingsRow]
        public var offersClear: Bool
        /// Shown instead of rows: History is unavailable, or empty.
        public var note: String?

        public init(rows: [SettingsRow], offersClear: Bool, note: String?) {
            self.rows = rows
            self.offersClear = offersClear
            self.note = note
        }
    }

    /// Said in Settings while History can't be had: the coordinator is off (or the core hasn't
    /// answered yet), and the core sends no sessions then.
    public static let unavailableText = "History needs the coordinator. Turn on Hand work to the coordinator under Coordinator."

    /// `available`: the coordinator is on. Off, no rows: the core's empty list means nothing then.
    public static func settingsList(available: Bool, _ refs: [TrayAppState.HistoryRef]) -> SettingsList {
        guard available else { return SettingsList(rows: [], offersClear: false, note: unavailableText) }
        return SettingsList(rows: settingsRows(refs), offersClear: offersClear(refs), note: refs.isEmpty ? emptyText : nil)
    }

    /// Clear History is offered while anything but running sessions is listed: a running one
    /// stays.
    public static func offersClear(_ refs: [TrayAppState.HistoryRef]) -> Bool {
        refs.contains { !$0.running }
    }
}
