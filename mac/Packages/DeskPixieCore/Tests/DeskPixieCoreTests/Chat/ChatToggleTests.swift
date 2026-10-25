import DeskPixieCore
import XCTest

final class ChatToggleTests: XCTestCase {
    func testTheButtonShowsWhenHiddenAndHidesWhenShown() {
        XCTAssertEqual(ChatToggle(shown: false, unread: 0).action, .show)
        XCTAssertEqual(ChatToggle(shown: false, unread: 3).action, .show)
        XCTAssertEqual(ChatToggle(shown: true, unread: 0).action, .hide)
        XCTAssertEqual(ChatToggle(shown: true, unread: 2).action, .hide)
        XCTAssertFalse(ChatToggle(shown: false, unread: 0).filled)
        XCTAssertTrue(ChatToggle(shown: true, unread: 0).filled, "filled while the window is shown")
    }

    func testItsNameSaysWhatAClickDoes() {
        XCTAssertEqual(ChatToggle(shown: false, unread: 0).label, "Show chat")
        XCTAssertEqual(ChatToggle(shown: true, unread: 0).label, "Hide chat")
        XCTAssertEqual(ChatToggle(shown: false, unread: 2).label, "Show chat, 2 unread")
        XCTAssertEqual(ChatToggle(shown: true, unread: 1).label, "Hide chat, 1 unread")
        XCTAssertEqual(ChatToggle(shown: false, unread: -1).label, "Show chat", "no count below one")
        XCTAssertEqual(ChatToggle(shown: false, unread: 2).help, "Show chat, 2 unread (⌘⇧C)")
    }

    func testTheDotIsForUnreadWhileHidden() {
        XCTAssertTrue(ChatToggle(shown: false, unread: 1).dot)
        XCTAssertFalse(ChatToggle(shown: false, unread: 0).dot)
        XCTAssertFalse(ChatToggle(shown: true, unread: 4).dot, "the window is there to read")
    }

    func testCmdShiftCIsTheShortcut() {
        let cmd: UInt = 1 << 20, shift: UInt = 1 << 17, caps: UInt = 1 << 16, opt: UInt = 1 << 19, ctrl: UInt = 1 << 18
        XCTAssertTrue(ChatToggle.isShortcut(flags: cmd | shift, key: "c"))
        XCTAssertTrue(ChatToggle.isShortcut(flags: cmd | shift, key: "C"))
        XCTAssertTrue(ChatToggle.isShortcut(flags: cmd | shift | caps, key: "c"))
        XCTAssertFalse(ChatToggle.isShortcut(flags: cmd, key: "c"), "⌘C copies")
        XCTAssertFalse(ChatToggle.isShortcut(flags: cmd | shift | opt, key: "c"))
        XCTAssertFalse(ChatToggle.isShortcut(flags: cmd | shift | ctrl, key: "c"))
        XCTAssertFalse(ChatToggle.isShortcut(flags: cmd | shift, key: "v"))
        XCTAssertFalse(ChatToggle.isShortcut(flags: shift, key: "c"))
        XCTAssertFalse(ChatToggle.isShortcut(flags: cmd | shift, key: nil))
    }

    /// The menu's Chat… shows the same key the toggle answers to.
    func testTheMenuShowsTheSameShortcut() {
        let rows = TrayMenu.build(nil, localStatusLines: [], micModeAvailable: false).compactMap { item -> TrayMenu.Row? in
            if case .action(let r) = item, r.id == TrayMenu.chatID { return r }
            return nil
        }
        XCTAssertEqual(rows.first?.shortcut, ChatToggle.shortcutKey)
        XCTAssertEqual(rows.first?.shift, true)
    }
}
