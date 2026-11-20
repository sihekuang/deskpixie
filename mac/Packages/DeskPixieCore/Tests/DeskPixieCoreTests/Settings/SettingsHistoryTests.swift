import Observation
import XCTest
@testable import DeskPixieCore

/// Settings ▸ Sessions ▸ History (#461): which buttons each session gets and the ids they send.
@MainActor
final class SettingsHistoryTests: XCTestCase {
    private let running = TrayAppState.HistoryRef(id: "s-run", label: "● login bug · deskpixie · running", running: true)
    private let past = TrayAppState.HistoryRef(id: "s-old", label: "login bug · deskpixie · 2h ago")
    private let gone = TrayAppState.HistoryRef(id: "s-gone", label: "tidy up · notes · 3d ago", enabled: false)

    func test_a_past_session_resumes_and_can_be_removed() {
        XCTAssertEqual(HistoryActions.settingsRows([past]), [
            .init(id: "s-old", label: "login bug · deskpixie · 2h ago", buttonTitle: "Resume",
                  buttonID: "agent:history:resume:s-old", buttonEnabled: true, removeID: "agent:history:remove:s-old"),
        ])
    }

    func test_a_running_session_shows_and_cannot_be_removed() {
        XCTAssertEqual(HistoryActions.settingsRows([running]), [
            .init(id: "s-run", label: "● login bug · deskpixie · running", buttonTitle: "Show",
                  buttonID: "agent:history:resume:s-run", buttonEnabled: true, removeID: nil),
        ])
    }

    func test_an_unresumable_session_has_resume_greyed_and_can_be_removed() {
        let row = HistoryActions.settingsRows([gone])[0]
        XCTAssertEqual(row.buttonTitle, "Resume")
        XCTAssertFalse(row.buttonEnabled)
        XCTAssertEqual(row.removeID, "agent:history:remove:s-gone")
    }

    func test_clear_is_offered_only_with_something_to_clear() {
        XCTAssertFalse(HistoryActions.offersClear([]))
        XCTAssertEqual(HistoryActions.emptyText, "No past sessions")
        XCTAssertFalse(HistoryActions.offersClear([running]), "a running session stays")
        XCTAssertTrue(HistoryActions.offersClear([running, gone]))
        XCTAssertEqual(HistoryActions.clearID, "agent:history:clear")
    }

    func test_the_list_says_when_history_is_empty_or_unavailable() {
        let off = HistoryActions.settingsList(available: false, [past])
        XCTAssertEqual(off, .init(rows: [], offersClear: false,
                                  note: "History needs the coordinator. Turn on Hand work to the coordinator under Coordinator."))
        XCTAssertEqual(HistoryActions.settingsList(available: true, []),
                       .init(rows: [], offersClear: false, note: "No past sessions"))
        let on = HistoryActions.settingsList(available: true, [running, past])
        XCTAssertEqual(on.rows, HistoryActions.settingsRows([running, past]))
        XCTAssertTrue(on.offersClear)
        XCTAssertNil(on.note)
    }

    func test_the_model_lists_the_tray_sessions_in_order() {
        let model = SettingsModel(client: FakeSettingsClient())
        XCTAssertEqual(model.history.rows, [])
        XCTAssertEqual(model.history.note, HistoryActions.unavailableText,
                       "until the app gives it the tray state (and before the core answers), History is unavailable, not empty")
        var refs = [running, past]
        var available = true
        model.historySource = { refs }
        model.historyAvailable = { available }
        XCTAssertEqual(model.history.rows.map(\.id), ["s-run", "s-old"])
        XCTAssertTrue(model.history.offersClear)
        // Read on every draw: a newer tray state shows at once.
        refs = []
        XCTAssertEqual(model.history.note, HistoryActions.emptyText)
        refs = [past]
        available = false
        XCTAssertEqual(model.history.rows, [])
        XCTAssertEqual(model.history.note, HistoryActions.unavailableText)
    }

    func test_a_view_reading_the_list_redraws_when_the_tray_state_changes() {
        let tray = TrayStub()
        let model = SettingsModel(client: FakeSettingsClient())
        model.historySource = { tray.refs }
        model.historyAvailable = { tray.available }
        let changed = Flag()
        let rows = withObservationTracking { model.history.rows } onChange: { changed.set() }
        XCTAssertEqual(rows, [])
        tray.refs = [past]
        XCTAssertTrue(changed.isSet, "onChange fires synchronously on the change")
        XCTAssertEqual(model.history.rows.map(\.id), ["s-old"])
    }

    func test_the_buttons_go_through_the_tray_path_not_settings_calls() {
        let client = FakeSettingsClient()
        let model = SettingsModel(client: client)
        model.historyAction("agent:history:clear") // no action hook yet: nothing happens
        var sent: [String] = []
        model.performAction = { sent.append($0) }
        model.historyAction(HistoryActions.resumeID("s-old"))
        model.historyAction(HistoryActions.removeID("s-old"))
        model.historyAction(HistoryActions.clearID)
        XCTAssertEqual(sent, ["agent:history:resume:s-old", "agent:history:remove:s-old", "agent:history:clear"])
        XCTAssertEqual(client.calls, [])
    }
}

/// Stands in for the app's tray state: observed, like `AppState`.
@MainActor
@Observable
private final class TrayStub {
    var refs: [TrayAppState.HistoryRef] = []
    var available = true
}

/// Set from `onChange`, which may be called off the main actor.
private final class Flag: @unchecked Sendable {
    private let lock = NSLock()
    private var value = false
    func set() { lock.withLock { value = true } }
    var isSet: Bool { lock.withLock { value } }
}
