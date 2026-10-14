import Carbon
import XCTest
@testable import DeskPixieCore

/// Ports of ai-mascot `src-tauri/src/settings/native/hotkey.rs` tests (8 → 8), plus tests for
/// `HotkeyRecorder.carbon`, the reverse mapping the app binds with.
///
/// Adapted: `every_recordable_combo_is_a_valid_shortcut` parsed each combo with
/// tauri-plugin-global-shortcut; here it must parse with `carbon(_:)` (what the app binds
/// through) back to the same key code.
final class SettingsHotkeyTests: XCTestCase {
    typealias H = HotkeyRecorder
    private let k: UInt16 = 0x28
    private let space: UInt16 = 0x31
    private let f5: UInt16 = 0x60

    func test_a_key_with_a_strong_modifier_is_recorded_in_the_web_format() {
        XCTAssertEqual(H.record(keyCode: space, flags: H.option | H.command), .combo("alt+super+Space"))
        XCTAssertEqual(H.record(keyCode: k, flags: H.command), .combo("super+KeyK"))
        XCTAssertEqual(H.record(keyCode: space, flags: H.control | H.option), .combo("ctrl+alt+Space"))
        XCTAssertEqual(H.record(keyCode: k, flags: H.command | H.shift | H.option | H.control),
                       .combo("ctrl+alt+shift+super+KeyK"))
        XCTAssertEqual(H.record(keyCode: 0x12, flags: H.control), .combo("ctrl+Digit1"))
        XCTAssertEqual(H.record(keyCode: 0x7E, flags: H.control | (1 << 23)), .combo("ctrl+ArrowUp"),
                       "fn and other flags are ignored")
    }

    func test_shift_alone_or_no_modifier_is_refused() {
        XCTAssertEqual(H.record(keyCode: k, flags: H.shift), .refused)
        XCTAssertEqual(H.record(keyCode: k, flags: 0), .refused)
        XCTAssertEqual(H.record(keyCode: space, flags: H.shift | (1 << 16)), .refused, "caps lock is not a modifier")
    }

    func test_function_keys_need_no_modifier() {
        XCTAssertEqual(H.record(keyCode: f5, flags: 0), .combo("F5"))
        XCTAssertEqual(H.record(keyCode: 0x5A, flags: H.shift), .combo("shift+F20"))
        XCTAssertEqual(H.record(keyCode: 0x7A, flags: 0), .combo("F1"))
    }

    func test_escape_cancels_whatever_is_held() {
        XCTAssertEqual(H.record(keyCode: H.escape, flags: 0), .cancel)
        XCTAssertEqual(H.record(keyCode: H.escape, flags: H.command), .cancel)
    }

    func test_modifier_keys_alone_are_ignored() {
        for key: UInt16 in 0x36...0x3F {
            XCTAssertEqual(H.record(keyCode: key, flags: H.command | H.shift), .ignored, "key code \(key)")
        }
        XCTAssertEqual(H.record(keyCode: 0x48, flags: H.command), .ignored, "volume up has no name")
    }

    func test_every_letter_and_digit_has_its_code() {
        let names = (UInt16(0)...0x7F).compactMap(H.codeName)
        XCTAssertEqual(names.filter { $0.hasPrefix("Key") }.sorted(), (65...90).map { "Key\(Character(UnicodeScalar($0)!))" })
        XCTAssertEqual(names.filter { $0.hasPrefix("Digit") }.sorted(), (0...9).map { "Digit\($0)" })
        XCTAssertEqual(names.filter(H.isFunctionKey).count, 20)
    }

    func test_every_recordable_combo_is_a_valid_shortcut() {
        for key in UInt16(0)...0x7F {
            guard case .combo(let c) = H.record(keyCode: key, flags: H.control | H.option) else { continue }
            guard let bound = H.carbon(c) else { XCTFail("\(c) does not parse"); continue }
            XCTAssertEqual(bound.keyCode, UInt32(key), c)
            XCTAssertEqual(bound.modifiers, UInt32(controlKey | optionKey), c)
        }
    }

    func test_recorded_combos_format_for_display() {
        guard case .combo(let c) = H.record(keyCode: space, flags: H.option | H.command) else { return XCTFail() }
        XCTAssertEqual(SettingsLayout.formatHotkey(c, platform: .macos), "⌥⌘Space")
    }

    // MARK: Swift-only: binding a stored combo

    func test_stored_combos_bind_in_any_spelling() {
        XCTAssertEqual(H.carbon("ctrl+alt+Space"), .init(keyCode: UInt32(kVK_Space), modifiers: UInt32(controlKey | optionKey)))
        XCTAssertEqual(H.carbon("Control+Option+space"), H.carbon("ctrl+alt+Space"))
        XCTAssertEqual(H.carbon("cmd+shift+K"), .init(keyCode: UInt32(kVK_ANSI_K), modifiers: UInt32(cmdKey | shiftKey)))
        XCTAssertEqual(H.carbon("CmdOrCtrl+1"), .init(keyCode: UInt32(kVK_ANSI_1), modifiers: UInt32(cmdKey)))
        XCTAssertEqual(H.carbon("alt+Up"), H.carbon("alt+ArrowUp"))
        XCTAssertEqual(H.carbon("F5"), .init(keyCode: UInt32(kVK_F5), modifiers: 0))
        XCTAssertEqual(H.carbon(" ctrl + alt + Space "), H.carbon("ctrl+alt+Space"))
    }

    func test_combos_this_mac_cannot_bind_are_nil() {
        XCTAssertNil(H.carbon(""))
        XCTAssertNil(H.carbon("ctrl+"))
        XCTAssertNil(H.carbon("ctrl+alt"))
        XCTAssertNil(H.carbon("hyper+K"))
        XCTAssertNil(H.carbon("ctrl+MediaPlay"))
        XCTAssertNil(H.carbon("ctrl+KeyAB"))
    }
}
