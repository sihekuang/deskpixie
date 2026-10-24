import Foundation

/// Pure: what the Settings window shows. Each `SettingsField` becomes one `ControlSpec`, and
/// each control's input becomes the JSON value `settings.set` takes. Port of ai-mascot
/// `src-tauri/src/settings/native/layout.rs` and the matching rules of
/// `ui/settings/settings-core.js`. No SwiftUI here, so it is tested on its own.
public enum SettingsLayout {
    public static let notAvailable = "(not available on this platform)"
    public static let notFound = "(not found)"
    /// The hotkey recorder's title while it waits for a key.
    public static let recording = "Type shortcut…"
    public static let whole = "Enter a whole number"
    public static let empty = "Can't be empty"
    public static let wrongInput = "This control can't take that input"
    /// The banner's lead text (Go `FileBroken`).
    public static let fileBroken = "settings.json has a syntax error; fix it or reset it"
    /// Shown under the hotkey when the app could not bind the recorded shortcut.
    public static let hotkeyInUse = "That shortcut is in use"
    /// The field whose pop-up menu uses the tray's size names.
    public static let sizeField = "scale"

    // MARK: Tabs

    /// A sidebar tile's colour, named here so the package stays free of SwiftUI; the app maps
    /// each case to the SwiftUI colour of the same name.
    public enum TabTint: String, CaseIterable, Sendable {
        case gray, red, purple, blue, orange, teal, green, mint, pink, indigo, cyan
    }

    /// One sidebar page: a section, its title, its SF Symbol and its tile colour.
    public struct Tab: Equatable, Sendable {
        public var section: SettingsSection
        public var title: String
        public var symbol: String
        public var tint: TabTint
    }

    /// The sidebar's pages, in order.
    public static let tabs: [Tab] = [
        Tab(section: .general, title: "General", symbol: "face.smiling", tint: .pink),
        Tab(section: .voice, title: "Voice", symbol: "waveform", tint: .purple),
        Tab(section: .coordinator, title: "Coordinator", symbol: "sparkles", tint: .orange),
        Tab(section: .sessions, title: "Sessions", symbol: "terminal", tint: .teal),
        Tab(section: .workers, title: "Workers", symbol: "person.2", tint: .indigo),
        Tab(section: .notifications, title: "Notifications", symbol: "bell.badge", tint: .red),
        Tab(section: .advanced, title: "Advanced", symbol: "gearshape.2", tint: .gray),
    ]

    /// The sidebar index of `section`'s page.
    public static func tabIndex(_ section: SettingsSection) -> Int? {
        tabs.firstIndex { $0.section == section }
    }

    /// The page title a search result names (the sidebar's, e.g. "Coordinator" for Agent).
    public static func tabTitle(_ section: SettingsSection) -> String {
        tabs.first { $0.section == section }?.title ?? section.rawValue
    }

    /// Where a search result is: its page, and its group when it has one ("Voice › Microphone
    /// and speaker", #290).
    public static func place(_ hit: SettingsHit) -> String {
        hit.group.isEmpty ? tabTitle(hit.section) : "\(tabTitle(hit.section)) › \(hit.group)"
    }

    /// The search's line when nothing matches (#287).
    public static func noResultsText(_ query: String) -> String {
        "No settings match “\(query.trimmingCharacters(in: .whitespaces))”."
    }

    /// Whether `platforms` includes `platform`; nil means everywhere.
    public static func availableOn(_ platforms: [SettingsPlatform]?, _ platform: SettingsPlatform) -> Bool {
        platforms?.contains(platform) ?? true
    }

    /// The fields of `section` that show on `platform`, in schema order. With `values`, a
    /// field whose `visibleWhen` doesn't hold for them is left out too.
    public static func fields(in section: SettingsSection, _ fields: [SettingsField],
                              platform: SettingsPlatform, values: JSONValue? = nil) -> [SettingsField] {
        fields.filter {
            $0.section == section && availableOn($0.platforms, platform) && (values.map($0.isVisible) ?? true)
        }
    }

    // MARK: Controls

    public enum ControlKind: Equatable, Sendable {
        case `switch`, menu, stepper, checks, text, path, hotkey
    }

    /// The control for a kind. Exhaustive, so a new kind does not compile until it is mapped.
    public static func controlKind(_ kind: SettingsKind) -> ControlKind {
        switch kind {
        case .bool: return .switch
        case .int(_, _, let menu): return menu ? .menu : .stepper
        case .choice: return .menu
        case .multiChoice: return .checks
        case .text: return .text
        case .path: return .path
        case .hotkey: return .hotkey
        }
    }

    public struct MenuItem: Equatable, Sendable {
        public var label: String
        /// What choosing it commits.
        public var value: JSONValue
        public var enabled: Bool

        public init(label: String, value: JSONValue, enabled: Bool) {
            self.label = label
            self.value = value
            self.enabled = enabled
        }
    }

    public struct CheckItem: Equatable, Sendable {
        public var label: String
        public var value: String
        public var checked: Bool
        public var enabled: Bool

        public init(label: String, value: String, checked: Bool, enabled: Bool) {
            self.label = label
            self.value = value
            self.checked = checked
            self.enabled = enabled
        }
    }

    public enum Control: Equatable, Sendable {
        case `switch`(on: Bool)
        case menu(items: [MenuItem], selected: Int?)
        case stepper(min: Int64, max: Int64, value: Int64, text: String)
        /// `current` is the stored list, in order; a toggle changes only its own entry.
        case checks(items: [CheckItem], current: [String])
        case text(text: String, optional: Bool)
        /// `path` is nil when unset.
        case path(path: String?, dir: Bool, optional: Bool)
        /// `combo` is the stored shortcut string; `title` its Mac symbols, e.g. "⌥⌘Space".
        case hotkey(combo: String, title: String)

        public var kind: ControlKind {
            switch self {
            case .switch: return .switch
            case .menu: return .menu
            case .stepper: return .stepper
            case .checks: return .checks
            case .text: return .text
            case .path: return .path
            case .hotkey: return .hotkey
            }
        }
    }

    /// One row of a tab: the label column, the control, and the line under it.
    public struct ControlSpec: Equatable, Sendable {
        public var path: String
        /// The label column's text, ending in a colon.
        public var label: String
        /// The accessibility label: the field's label as written.
        public var a11yLabel: String
        /// The help line, which is also the accessibility help.
        public var help: String
        /// Shown in red in place of the help line until the next good commit.
        public var error: String?
        public var control: Control

        /// The line under the control and whether it is an error.
        public var note: (text: String, isError: Bool) {
            if let error { return (error, true) }
            return (help, false)
        }
    }

    public static func labelText(_ label: String) -> String { "\(label):" }

    /// Text shown for a value (web `toText`): null and missing are empty.
    public static func textOf(_ value: JSONValue?) -> String {
        switch value {
        case nil, .null?: return ""
        case .string(let s)?: return s
        case let v?: return v.description
        }
    }

    private static func unavailableLabel(_ all: [SettingsOption], _ value: String) -> String {
        if let known = all.first(where: { $0.value == value }) {
            return "\(known.label) \(notAvailable)"
        }
        return "\(value) \(notFound)"
    }

    /// The pop-up's items for a choice. A current value that isn't offered here stays visible
    /// as a disabled, labelled item, so it is never changed silently.
    public static func choiceItems(_ options: [SettingsOption], platform: SettingsPlatform,
                                   current: String) -> [MenuItem] {
        var items = options.filter { availableOn($0.platforms, platform) }.map {
            // An empty value ("System Default") stands for null.
            MenuItem(label: $0.label, value: $0.value.isEmpty ? .null : .string($0.value), enabled: true)
        }
        if !current.isEmpty, !items.contains(where: { $0.value.stringValue == current }) {
            items.append(MenuItem(label: unavailableLabel(options, current), value: .string(current), enabled: false))
        }
        return items
    }

    /// The size menu's names for the tray's sizes.
    private static func sizeName(_ n: Int64) -> String? {
        switch n {
        case 4: return "Small"
        case 6: return "Medium"
        case 8: return "Large"
        case 12: return "Huge"
        default: return nil
        }
    }

    /// Every whole number from `min` to `max`. The size field reads "Large (×8)" or "×5".
    public static func intItems(path: String, min: Int64, max: Int64) -> [MenuItem] {
        guard min <= max else { return [] }
        return (min...max).map { n in
            let label: String
            if path == sizeField {
                label = sizeName(n).map { "\($0) (×\(n))" } ?? "×\(n)"
            } else {
                label = String(n)
            }
            return MenuItem(label: label, value: .int(n), enabled: true)
        }
    }

    public static func checkItems(_ options: [SettingsOption], platform: SettingsPlatform,
                                  current: [String]) -> [CheckItem] {
        var items = options.filter { availableOn($0.platforms, platform) }.map {
            CheckItem(label: $0.label, value: $0.value, checked: current.contains($0.value), enabled: true)
        }
        for value in current where !items.contains(where: { $0.value == value }) {
            items.append(CheckItem(label: unavailableLabel(options, value), value: value, checked: true, enabled: false))
        }
        return items
    }

    /// `current` with `value` switched on or off; every other entry stays as it was (web `toggleMulti`).
    public static func toggleMulti(_ current: [String]?, _ value: String, on: Bool) -> [String] {
        let rest = (current ?? []).filter { $0 != value }
        return on ? rest + [value] : rest
    }

    /// The field's row, from the snapshot's values.
    public static func controlSpec(_ field: SettingsField, values: JSONValue, platform: SettingsPlatform,
                                   error: String? = nil) -> ControlSpec {
        let value = values.at(field.path)
        let control: Control
        switch field.kind {
        case .bool:
            control = .switch(on: value?.boolValue ?? false)
        case .int(let min, let max, true):
            let items = intItems(path: field.path, min: min, max: max)
            control = .menu(items: items, selected: value.flatMap { v in items.firstIndex { $0.value == v } })
        case .int(let min, let max, false):
            let n = value?.intValue ?? min
            control = .stepper(min: min, max: max, value: Swift.min(Swift.max(n, min), max), text: textOf(value))
        case .choice(let options):
            let current = textOf(value)
            let items = choiceItems(options, platform: platform, current: current)
            control = .menu(items: items, selected: items.firstIndex { textOf($0.value) == current })
        case .multiChoice(let options):
            let current = value?.arrayValue?.compactMap(\.stringValue) ?? []
            control = .checks(items: checkItems(options, platform: platform, current: current), current: current)
        case .text(let optional):
            control = .text(text: textOf(value), optional: optional)
        case .path(let dir, let optional):
            let p = textOf(value)
            control = .path(path: p.isEmpty ? nil : p, dir: dir, optional: optional)
        case .hotkey:
            let combo = textOf(value)
            control = .hotkey(combo: combo, title: formatHotkey(combo, platform: platform))
        }
        return ControlSpec(path: field.path, label: labelText(field.label), a11yLabel: field.label, help: field.help,
                           error: error, control: control)
    }

    // MARK: Input

    /// What the user did to a control.
    public enum Input: Equatable, Sendable {
        case `switch`(Bool)
        /// The index of the chosen pop-up item.
        case menu(Int)
        /// Text typed in a text or number field.
        case text(String)
        /// The stepper's new value.
        case step(Int64)
        /// A checkbox turned on or off.
        case check(index: Int, on: Bool)
        /// A path picked in the open panel, or nil for Clear.
        case path(String?)
        /// A recorded shortcut string.
        case hotkey(String)

        /// The text a refused value leaves in the field.
        public var typed: String? {
            switch self {
            case .text(let t): return t
            case .path(let p?): return p
            default: return nil
            }
        }
    }

    private static func wholeNumber(_ text: String) -> Int64? {
        let digits = text.hasPrefix("-") ? String(text.dropFirst()) : text
        guard !digits.isEmpty, digits.allSatisfy({ $0.isASCII && $0.isNumber }) else { return nil }
        return Int64(text)
    }

    private static func textValue(_ text: String, optional: Bool) -> Result<JSONValue, CommitError> {
        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty { return optional ? .success(.null) : .failure(CommitError(empty)) }
        return .success(.string(t))
    }

    /// Why an input could not become a value; shown under the field.
    public struct CommitError: Error, Equatable, Sendable {
        public var message: String
        public init(_ message: String) { self.message = message }
    }

    /// The JSON `settings.set` takes for `input`. Ranges and choices are checked by the core.
    public static func commitValue(_ spec: ControlSpec, _ input: Input) -> Result<JSONValue, CommitError> {
        switch (spec.control, input) {
        case (.switch, .switch(let on)):
            return .success(.bool(on))
        case (.menu(let items, _), .menu(let i)):
            guard items.indices.contains(i), items[i].enabled else { return .failure(CommitError(notAvailable)) }
            return .success(items[i].value)
        case (.stepper, .step(let n)):
            return .success(.int(n))
        case (.stepper, .text(let text)):
            guard let n = wholeNumber(text.trimmingCharacters(in: .whitespacesAndNewlines)) else {
                return .failure(CommitError(whole))
            }
            return .success(.int(n))
        case (.checks(let items, let current), .check(let index, let on)):
            guard items.indices.contains(index) else { return .failure(CommitError(wrongInput)) }
            return .success(.array(toggleMulti(current, items[index].value, on: on).map(JSONValue.string)))
        case (.text(_, let optional), .text(let text)):
            return textValue(text, optional: optional)
        case (.path(_, _, let optional), .path(let path)):
            return textValue(path ?? "", optional: optional)
        case (.hotkey, .hotkey(let combo)):
            return .success(.string(combo))
        default:
            return .failure(CommitError(wrongInput))
        }
    }

    /// Whether text a field sends when editing ends is worth committing. `shown` is the text the
    /// window last wrote into the field: an untouched field (perhaps showing a value that has
    /// since changed elsewhere) must not commit it back. A kept draft or a shown error is always
    /// retried.
    public static func shouldCommit(typed: String, shown: String, hasDraft: Bool, hasError: Bool) -> Bool {
        hasDraft || hasError || typed != shown
    }

    // MARK: Hotkey text

    private static func macSymbol(_ modifier: String) -> String? {
        switch modifier.lowercased() {
        case "ctrl", "control": return "⌃"
        case "alt", "option": return "⌥"
        case "shift": return "⇧"
        case "super", "cmd", "command": return "⌘"
        default: return nil
        }
    }

    private static let namedKeys: [String: String] = [
        "Minus": "-", "Equal": "=", "Comma": ",", "Period": ".", "Slash": "/", "Backslash": "\\",
        "Semicolon": ";", "Quote": "'", "Backquote": "`", "BracketLeft": "[", "BracketRight": "]", "Space": "Space",
    ]

    private static func keyName(_ code: String, platform: SettingsPlatform) -> String {
        if let n = namedKeys[code] { return n }
        let arrows: [String: String] = platform == .macos
            ? ["ArrowUp": "↑", "ArrowDown": "↓", "ArrowLeft": "←", "ArrowRight": "→"]
            : ["ArrowUp": "Up", "ArrowDown": "Down", "ArrowLeft": "Left", "ArrowRight": "Right"]
        if let a = arrows[code] { return a }
        func single(_ prefix: String, _ ok: (Character) -> Bool) -> String? {
            guard code.hasPrefix(prefix) else { return nil }
            let rest = code.dropFirst(prefix.count)
            guard rest.count == 1, let c = rest.first, ok(c) else { return nil }
            return String(rest)
        }
        return single("Key") { $0.isASCII && $0.isUppercase }
            ?? single("Digit") { $0.isASCII && $0.isNumber }
            ?? code
    }

    /// A shortcut string for people (web `formatHotkey`): "⌥⌘Space" on macOS, "Ctrl+Alt+K"
    /// elsewhere.
    public static func formatHotkey(_ combo: String, platform: SettingsPlatform) -> String {
        var parts = combo.split(separator: "+", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
        guard let code = parts.popLast() else { return "" }
        let key = keyName(code, platform: platform)
        if platform == .macos {
            return parts.map { macSymbol($0) ?? $0 }.joined() + key
        }
        let superName = platform == .windows ? "Win" : "Super"
        var out = parts.map { p -> String in
            switch p.lowercased() {
            case "ctrl", "control": return "Ctrl"
            case "alt", "option": return "Alt"
            case "shift": return "Shift"
            case "super", "cmd", "command": return superName
            default: return p
            }
        }
        out.append(key)
        return out.joined(separator: "+")
    }

    // MARK: Chrome

    /// The file-error banner's text.
    public static func bannerText(_ fileError: String) -> String {
        "\(fileBroken) (\(fileError))"
    }

    /// "Restart to apply: Voice service, Model", or nil when nothing waits for a restart.
    public static func restartText(_ pending: [String], fields: [SettingsField]) -> String? {
        guard !pending.isEmpty else { return nil }
        let labels = pending.map { p in fields.first { $0.path == p }?.label ?? p }
        return "Restart to apply: " + labels.joined(separator: ", ")
    }

    /// A failed save or action, shown above the Restart bar.
    public static func saveNotice(_ message: String) -> String {
        "Couldn't save settings: \(message)"
    }

    /// Where each error of a refused commit of `path` goes: under its own field, and when none
    /// is for `path` itself, all of them under `path` too, each named.
    public static func placeErrors(_ path: String, _ errors: [SettingsFieldError],
                                   fields: [SettingsField]) -> [(path: String, message: String)] {
        var out = errors.map { (path: $0.path, message: $0.message) }
        if !errors.contains(where: { $0.path == path }) {
            let label = { (p: String) in fields.first { $0.path == p }?.label ?? p }
            out.append((path, errors.map { "\(label($0.path)): \($0.message)" }.joined(separator: " ")))
        }
        return out
    }

    /// Whether a snapshot numbered `next` should replace the one shown (numbered `shown`).
    public static func isNewer(_ shown: UInt64?, _ next: UInt64) -> Bool {
        guard let shown else { return true }
        return next > shown
    }

    /// Whether a path is absolute (web `isAbsolutePath`): the open panel starts there.
    public static func isAbsolutePath(_ p: String?) -> Bool {
        guard let p else { return false }
        if p.hasPrefix("/") || p.hasPrefix("\\\\") { return true }
        let chars = Array(p)
        return chars.count >= 3 && chars[0].isASCII && chars[0].isLetter && chars[1] == ":"
            && (chars[2] == "\\" || chars[2] == "/")
    }

    // MARK: Extra rows

    public enum Action: String, Equatable, Sendable { case resetPosition, openFile, restartCoordinator, exportLogs }

    /// The rows a section has besides its fields.
    public enum Extra: Equatable, Sendable {
        case button(label: String, help: String, title: String, action: Action)
        /// An API key's row: a secure field with Save / Remove, and where the key comes from;
        /// never the key.
        case secret(id: SettingsSecretID, label: String, help: String)
        /// The pix command row (#89): install state, Install / Uninstall, the PATH line, Copy Aliases.
        case pix(help: String)

        /// The row's id, as the core's search names it (Go `settings.Extras`, path `extra:<id>`).
        public var id: String {
            switch self {
            case .button(_, _, _, let action): return action.rawValue
            case .secret(let id, _, _): return "key.\(id.rawValue)"
            case .pix: return "pix"
            }
        }

        /// The row's search path (`SettingsHit.path`).
        public var path: String { "extra:\(id)" }

        /// The row's name, as the core's search has it.
        public var label: String {
            switch self {
            case .button(let label, _, _, _), .secret(_, let label, _): return label
            case .pix: return "pix command"
            }
        }

        /// The row's help, as the core's search has it.
        public var help: String {
            switch self {
            case .button(_, let help, _, _), .secret(_, _, let help), .pix(let help): return help
            }
        }
    }

    /// Export Logs…'s suggested file name (#225), e.g. "DeskPixie logs 2026-10-21 1402.zip".
    public static func logExportName(_ date: Date, timeZone: TimeZone = .current) -> String {
        let f = DateFormatter()
        f.locale = Locale(identifier: "en_US_POSIX")
        f.timeZone = timeZone
        f.dateFormat = "yyyy-MM-dd HHmm"
        return "DeskPixie logs \(f.string(from: date)).zip"
    }

    /// The pix row's help.
    public static let pixHelp = "Start Claude Code or Codex with `pix claude` or `pix codex` in any terminal (WebStorm, iTerm, …) and Pixie can see, message and interrupt that session. Quitting DeskPixie never ends it."

    /// Every extra row's look; the core places them (`SettingsSnapshot.extras`).
    public static let allExtras: [Extra] = [
        .button(label: "Reset position", help: "Move Pixie back to the bottom-right corner of the main screen.",
                title: "Reset Position", action: .resetPosition),
    ] + SettingsSecretID.allCases.map { .secret(id: $0, label: $0.label, help: $0.help) } + [
        .button(label: "Restart coordinator",
                help: "Stops the coordinator and starts it again on your next request, with its conversation. Asks first.",
                title: "Restart Coordinator…", action: .restartCoordinator),
        .pix(help: pixHelp),
        .button(label: "Settings file", help: "settings.json holds every setting.",
                title: "Open Settings File", action: .openFile),
        .button(label: "Logs",
                help: "Saves the app's log, pix's log, the voice log and metrics in one zip to share. It can include what you said to Pixie.",
                title: "Export Logs…", action: .exportLogs),
    ]

    /// The extra row with `id`; nil for one this app doesn't draw.
    public static func extra(_ id: String) -> Extra? {
        allExtras.first { $0.id == id }
    }

    // MARK: Groups (#290)

    /// One row of a page: a setting, or one of the window's other rows.
    public enum Row: Equatable, Sendable {
        case field(SettingsField)
        case extra(Extra)

        /// The row's id: the field's path, or the extra's search path.
        public var id: String {
            switch self {
            case .field(let f): return f.path
            case .extra(let e): return e.path
            }
        }
    }

    /// A heading and the rows under it.
    public struct Group: Equatable, Sendable {
        public var title: String
        public var rows: [Row]
    }

    /// `fields` (a page's, already filtered) and `section`'s extra rows, in groups in the order
    /// they first appear; each group's extras end it, and a group with only extras comes last.
    public static func groups(in section: SettingsSection, fields: [SettingsField],
                              extras: [SettingsExtraPlace]) -> [Group] {
        var out: [Group] = []
        func add(_ row: Row, to title: String) {
            if let i = out.firstIndex(where: { $0.title == title }) {
                out[i].rows.append(row)
            } else {
                out.append(Group(title: title, rows: [row]))
            }
        }
        for f in fields { add(.field(f), to: f.group) }
        for place in extras where place.section == section {
            if let e = extra(place.id) { add(.extra(e), to: place.group) }
        }
        return out
    }

    /// Under a greyed-out field: which switch turns it on, e.g. "Turn on Workers to change this."
    public static func disabledNote(_ field: SettingsField, fields: [SettingsField], values: JSONValue) -> String? {
        guard let off = (field.enabledWhen ?? []).first(where: { !$0.holds(values) }) else { return nil }
        let name = fields.first { $0.path == off.key }?.label ?? off.key
        return "Turn on “\(name)” to change this."
    }

    /// "From environment", "From Keychain", "From .env" (or "Found" when the core names no
    /// source), "Missing", or "Unknown" (the status could not be read).
    public static func secretText(_ secrets: SettingsSecrets?, _ id: SettingsSecretID) -> String {
        switch secrets?[id] {
        case .found?:
            switch secrets?.source(id) {
            case "env"?: return "From environment"
            case "keychain"?: return "From Keychain"
            case "dotenv"?: return "From .env"
            default: return "Found"
            }
        case .missing?: return "Missing"
        case nil: return "Unknown"
        }
    }

    /// Save is enabled once something other than whitespace is typed.
    public static func canSaveKey(_ typed: String) -> Bool {
        !typed.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    /// The key row's error when the Keychain refuses a save.
    public static let keySaveError = "Couldn't save to Keychain"
    /// The key row's error when the Keychain refuses a remove.
    public static let keyRemoveError = "Couldn't remove from Keychain"
}

/// The API keys the Voice tab reports, in order. Never the keys themselves.
public enum SettingsSecretID: String, CaseIterable, Sendable {
    case openAI = "openai"
    case gemini = "gemini"

    public var label: String {
        switch self {
        case .openAI: return "OpenAI key"
        case .gemini: return "Gemini key"
        }
    }

    public var variable: String {
        switch self {
        case .openAI: return "OPENAI_API_KEY"
        case .gemini: return "GEMINI_API_KEY"
        }
    }

    public var help: String {
        "Type the key here to keep it in the Keychain (used first), or set \(variable) in your environment or a .env file. "
            + "Keys are never shown here."
    }
}
