/// The round button above Pixie that shows and hides the chat window (#302), and ⌘⇧C, which
/// does the same while DeskPixie is in front: what a click does, what it's called, and the
/// dot for something you haven't read. The menu bar's Chat… only ever shows the window.
public struct ChatToggle: Equatable, Sendable {
    public enum Action: Equatable, Sendable { case show, hide }

    /// The key the menu's Chat… shows, with ⌘⇧ (`TrayMenu`).
    public static let shortcutKey: Character = "c"

    /// The window is on screen: not closed, hidden, minimized, covered or on another Space.
    public let shown: Bool
    /// Entries you haven't read, as the core counts them.
    public let unread: Int

    public init(shown: Bool, unread: Int) {
        self.shown = shown
        self.unread = unread
    }

    /// Shown, for the button and for what counts as read: ordered in (`visible`), some of it
    /// not covered by other windows or on another Space (`unoccluded`), and not minimized. A
    /// window buried under others is not shown: a click brings it to the front.
    public static func isOnScreen(visible: Bool, unoccluded: Bool, miniaturized: Bool) -> Bool {
        visible && unoccluded && !miniaturized
    }

    public var action: Action { shown ? .hide : .show }

    /// Filled with the accent colour while the window is shown.
    public var filled: Bool { shown }

    /// The VoiceOver name: what a click does, and how many are unread.
    public var label: String {
        let verb = shown ? "Hide chat" : "Show chat"
        return unread > 0 ? "\(verb), \(unread) unread" : verb
    }

    /// The tooltip: the name and the shortcut.
    public var help: String { "\(label) (⌘⇧\(Self.shortcutKey.uppercased()))" }

    /// A dot while there's something unread and the window isn't there to show it.
    public var dot: Bool { !shown && unread > 0 }

    /// ⌘⇧C, with no other modifier (Caps Lock and Fn don't count). `flags` are
    /// NSEvent.ModifierFlags' raw value; `key` is charactersIgnoringModifiers.
    public static func isShortcut(flags: UInt, key: String?) -> Bool {
        let shift: UInt = 1 << 17, control: UInt = 1 << 18, option: UInt = 1 << 19, command: UInt = 1 << 20
        return flags & (shift | control | option | command) == command | shift
            && key?.lowercased() == String(shortcutKey)
    }
}
