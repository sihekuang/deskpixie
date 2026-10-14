import Carbon
import Foundation

/// Pure: a macOS key-down (virtual key code plus modifier flags) as the shortcut string
/// settings.json stores, with the web recorder's rules: `mods+Code`, mods in the order ctrl,
/// alt, shift, super, and `Code` the W3C key code name (`KeyK`, `Space`, `F5`). Port of
/// ai-mascot `src-tauri/src/settings/native/hotkey.rs`, plus the reverse direction
/// (`carbon(_:)`) so the app can bind a stored combo with `CarbonHotkey`.
public enum HotkeyRecorder {
    /// `NSEvent.ModifierFlags` bits (AppKit's values).
    public static let shift: UInt64 = 1 << 17
    public static let control: UInt64 = 1 << 18
    public static let option: UInt64 = 1 << 19
    public static let command: UInt64 = 1 << 20

    /// `kVK_Escape`.
    public static let escape: UInt16 = 0x35

    /// What the recorder does with a key-down.
    public enum Recorded: Equatable, Sendable {
        /// A usable combo, e.g. `alt+super+Space`.
        case combo(String)
        /// Esc: stop recording and keep the old hotkey.
        case cancel
        /// A key without ⌃, ⌥ or ⌘ (shift alone included), and not a function key: refused,
        /// and recording goes on.
        case refused
        /// A modifier key on its own, or a key with no name: keep waiting.
        case ignored
    }

    /// Virtual key code (`kVK_*`) → W3C code name. Modifier keys (0x36…0x3F) and keys the
    /// shortcut grammar can't name (ISO §, JIS ¥ and _, volume keys) are absent.
    private static let codes: [UInt16: String] = [
        0x00: "KeyA", 0x01: "KeyS", 0x02: "KeyD", 0x03: "KeyF", 0x04: "KeyH", 0x05: "KeyG", 0x06: "KeyZ",
        0x07: "KeyX", 0x08: "KeyC", 0x09: "KeyV", 0x0B: "KeyB", 0x0C: "KeyQ", 0x0D: "KeyW", 0x0E: "KeyE",
        0x0F: "KeyR", 0x10: "KeyY", 0x11: "KeyT", 0x12: "Digit1", 0x13: "Digit2", 0x14: "Digit3",
        0x15: "Digit4", 0x16: "Digit6", 0x17: "Digit5", 0x18: "Equal", 0x19: "Digit9", 0x1A: "Digit7",
        0x1B: "Minus", 0x1C: "Digit8", 0x1D: "Digit0", 0x1E: "BracketRight", 0x1F: "KeyO", 0x20: "KeyU",
        0x21: "BracketLeft", 0x22: "KeyI", 0x23: "KeyP", 0x24: "Enter", 0x25: "KeyL", 0x26: "KeyJ",
        0x27: "Quote", 0x28: "KeyK", 0x29: "Semicolon", 0x2A: "Backslash", 0x2B: "Comma", 0x2C: "Slash",
        0x2D: "KeyN", 0x2E: "KeyM", 0x2F: "Period", 0x30: "Tab", 0x31: "Space", 0x32: "Backquote",
        0x33: "Backspace", 0x35: "Escape", 0x40: "F17", 0x41: "NumpadDecimal", 0x43: "NumpadMultiply",
        0x45: "NumpadAdd", 0x47: "NumLock", 0x4B: "NumpadDivide", 0x4C: "NumpadEnter", 0x4E: "NumpadSubtract",
        0x4F: "F18", 0x50: "F19", 0x51: "NumpadEqual", 0x52: "Numpad0", 0x53: "Numpad1", 0x54: "Numpad2",
        0x55: "Numpad3", 0x56: "Numpad4", 0x57: "Numpad5", 0x58: "Numpad6", 0x59: "Numpad7", 0x5A: "F20",
        0x5B: "Numpad8", 0x5C: "Numpad9", 0x60: "F5", 0x61: "F6", 0x62: "F7", 0x63: "F3", 0x64: "F8",
        0x65: "F9", 0x67: "F11", 0x69: "F13", 0x6A: "F16", 0x6B: "F14", 0x6D: "F10", 0x6F: "F12",
        0x71: "F15", 0x72: "Insert", 0x73: "Home", 0x74: "PageUp", 0x75: "Delete", 0x76: "F4", 0x77: "End",
        0x78: "F2", 0x79: "PageDown", 0x7A: "F1", 0x7B: "ArrowLeft", 0x7C: "ArrowRight", 0x7D: "ArrowDown",
        0x7E: "ArrowUp",
    ]

    /// The W3C `code` name of a macOS virtual key code, or nil for modifier keys and keys the
    /// shortcut grammar can't name.
    public static func codeName(_ keyCode: UInt16) -> String? { codes[keyCode] }

    static func isFunctionKey(_ code: String) -> Bool {
        guard code.hasPrefix("F") else { return false }
        let n = code.dropFirst()
        return (1...2).contains(n.count) && n.allSatisfy { $0.isASCII && $0.isNumber }
    }

    /// The recorder's verdict on one key-down.
    public static func record(keyCode: UInt16, flags: UInt64) -> Recorded {
        if keyCode == escape { return .cancel }
        guard let code = codeName(keyCode) else { return .ignored }
        let ctrl = flags & control != 0, alt = flags & option != 0
        let sh = flags & shift != 0, cmd = flags & command != 0
        if !(ctrl || alt || cmd) && !isFunctionKey(code) { return .refused }
        let mods: [(Bool, String)] = [(ctrl, "ctrl"), (alt, "alt"), (sh, "shift"), (cmd, "super")]
        return .combo((mods.filter(\.0).map(\.1) + [code]).joined(separator: "+"))
    }

    // MARK: Binding

    /// A combo as `RegisterEventHotKey` takes it.
    public struct CarbonCombo: Equatable, Sendable {
        public var keyCode: UInt32
        public var modifiers: UInt32
    }

    /// Upper-cased key names (the grammar is case-insensitive) → virtual key code, including
    /// the short spellings the grammar allows ("K", "1", "Up", "Esc", "NumAdd").
    private static let keyCodes: [String: UInt16] = {
        var m: [String: UInt16] = [:]
        for (code, name) in codes {
            let upper = name.uppercased()
            m[upper] = code
            if upper.hasPrefix("KEY") { m[String(upper.dropFirst(3))] = code }
            if upper.hasPrefix("DIGIT") { m[String(upper.dropFirst(5))] = code }
            if upper.hasPrefix("ARROW") { m[String(upper.dropFirst(5))] = code }
            if upper.hasPrefix("NUMPAD") { m["NUM" + upper.dropFirst(6)] = code }
        }
        let aliases: [String: String] = [
            "ESC": "ESCAPE", "`": "BACKQUOTE", "\\": "BACKSLASH", "[": "BRACKETLEFT", "]": "BRACKETRIGHT",
            ",": "COMMA", "=": "EQUAL", "-": "MINUS", ".": "PERIOD", "'": "QUOTE", ";": "SEMICOLON",
            "/": "SLASH", "NUMPADPLUS": "NUMPADADD", "NUMPLUS": "NUMPADADD",
        ]
        for (alias, name) in aliases { m[alias] = m[name] }
        return m
    }()

    /// A stored combo (`ctrl+alt+Space`, any case, `cmd`/`option`/`cmdorctrl` spellings) as a
    /// Carbon key code and modifier mask, or nil when this Mac can't bind it.
    public static func carbon(_ combo: String) -> CarbonCombo? {
        let tokens = combo.split(separator: "+", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }
        guard let last = tokens.last, !last.isEmpty, let key = keyCodes[last.uppercased()] else { return nil }
        var mods: UInt32 = 0
        for token in tokens.dropLast() {
            switch token.uppercased() {
            case "CTRL", "CONTROL": mods |= UInt32(controlKey)
            case "ALT", "OPTION": mods |= UInt32(optionKey)
            case "SHIFT": mods |= UInt32(shiftKey)
            case "SUPER", "CMD", "COMMAND", "COMMANDORCONTROL", "COMMANDORCTRL", "CMDORCTRL", "CMDORCONTROL":
                mods |= UInt32(cmdKey)
            default: return nil
            }
        }
        return CarbonCombo(keyCode: UInt32(key), modifiers: mods)
    }
}
