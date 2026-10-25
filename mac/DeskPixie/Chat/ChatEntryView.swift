import AppKit
import DeskPixieCore
import SwiftUI

/// The chat window's colours (#302), from the approved mockup, each with a dark-mode twin.
enum ChatColors {
    static let bubble = adaptive(0x0A66D8, 0x0A66D8)
    /// Your words still coming: a lighter bubble than the sent one.
    static let liveBubble = adaptive(0xD8E7FB, 0x1D3A5E)
    static let link = adaptive(0x0A5FC4, 0x5AA2F5)
    static let label = adaptive(0x5C5C61, 0xA1A1A6)
    static let faint = adaptive(0x6E6E73, 0x8E8E93)
    static let border = adaptive(0xC7C7CC, 0x48484A)
    static let rule = adaptive(0xE5E5EA, 0x38383A)
    static let field = adaptive(0xF1F1F3, 0x2C2C2E)
    // The needs strip and the cards that need you.
    static let needsStrip = adaptive(0xFFF4E5, 0x3A2A14)
    static let needsRule = adaptive(0xF5D9B0, 0x5C4220)
    static let needsTitle = adaptive(0x8A4500, 0xF5B66B)
    static let needsPillText = adaptive(0x6B3600, 0xF2C894)
    static let needsEdge = adaptive(0xF0C890, 0x7A5628)
    static let needsCard = adaptive(0xFFFAF2, 0x2A2118)
    static let pill = adaptive(0xFFFFFF, 0x1C1C1E)

    /// A session's dot, by its `color` (0..5, the same for a name all day). They differ in
    /// lightness as well as hue.
    static let sessions: [Color] = [
        adaptive(0x1F9D55, 0x34C77B), adaptive(0x0E7490, 0x22B5D6), adaptive(0xC2410C, 0xF97A3D),
        adaptive(0x7C3AED, 0xA78BFA), adaptive(0xBE185D, 0xF472B6), adaptive(0x1D4ED8, 0x60A5FA),
    ]

    static func session(_ color: Int) -> Color { sessions[((color % sessions.count) + sessions.count) % sessions.count] }

    private static func adaptive(_ light: UInt32, _ dark: UInt32) -> Color {
        Color(nsColor: NSColor(name: nil) { appearance in
            let hex = appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? dark : light
            return NSColor(srgbRed: CGFloat((hex >> 16) & 0xFF) / 255, green: CGFloat((hex >> 8) & 0xFF) / 255,
                           blue: CGFloat(hex & 0xFF) / 255, alpha: 1)
        })
    }
}

/// What a row can ask of the window.
struct ChatActions {
    /// `agent.focusSession` for a session's address (a chip under Pixie's reply).
    let focusSession: (String) -> Void
    /// The terminal button beside a responder's name.
    let focus: (ChatTerminalTarget) -> Void
    let decide: (String, Bool) -> Void
    /// The option counts from 1.
    let choose: (String, Int) -> Void
    let reply: (ChatEntry) -> Void
    /// The stop button by a working responder's name (#309), on its newest entry; and who
    /// said an entry with its status now (the model's roster).
    var stopTarget: (ChatEntry) -> ChatStopTarget? = { _ in nil }
    var liveWho: (ChatEntry) -> ChatWho = { $0.who }
    var stop: (ChatStopTarget) -> Void = { _ in }
}

/// One message in the thread, drawn by who said it (spec §4.1): your bubble on the right,
/// Pixie's and the coordinator's words under their name, a session's under its header, and a
/// request as a card, or its outcome line once answered.
struct ChatEntryView: View {
    let entry: ChatEntry
    let actions: ChatActions
    /// Looks a session's address up for its chip.
    let who: (String) -> ChatWho

    static let font = NSFont.systemFont(ofSize: 14)

    var body: some View {
        Group {
            if entry.who.kind == "you" {
                yours
            } else if entry.request != nil {
                if ChatThreadModel.isOpen(entry) {
                    ChatRequestCard(entry: entry, actions: actions)
                } else {
                    outcome
                }
            } else if entry.who.kind == "session" {
                session
            } else {
                spoken
            }
        }
        .frame(maxWidth: .infinity, alignment: entry.who.kind == "you" ? .trailing : .leading)
    }

    /// Yours: a blue bubble on the right; a mic mark when spoken; attachments under it.
    private var yours: some View {
        VStack(alignment: .trailing, spacing: 4) {
            Text(entry.text)
                .font(.system(size: 14))
                .lineSpacing(2)
                .foregroundStyle(.white)
                .textSelection(.enabled)
                .padding(.horizontal, 14)
                .padding(.vertical, 10)
                .background(UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18,
                                                   bottomTrailingRadius: 4, topTrailingRadius: 18)
                    .fill(ChatColors.bubble))
            if !entry.attachments.isEmpty {
                SentAttachments(paths: entry.attachments)
            }
            if entry.spoken {
                HStack(spacing: 4) {
                    Image(systemName: "mic")
                        .font(.system(size: 10))
                    Text("spoken")
                }
                .font(.system(size: 11))
                .foregroundStyle(ChatColors.faint)
                .accessibilityElement(children: .combine)
            }
        }
        .padding(.leading, 120)
    }

    /// "Pixie" or "Coordinator" over the words (the coordinator with its terminal button);
    /// under a reply, the sessions it mentions.
    private var spoken: some View {
        VStack(alignment: .leading, spacing: entry.sessions.isEmpty ? 4 : 6) {
            HStack(spacing: 4) {
                Text(ChatThreadModel.header(entry.who))
                    .font(.system(size: 12, weight: .semibold))
                    .foregroundStyle(ChatColors.label)
                if let target = ChatThreadModel.terminalTarget(for: entry) {
                    TerminalButton(target: target) { actions.focus(target) }
                }
                if let stop = actions.stopTarget(entry) {
                    StopButton(target: stop) { actions.stop(stop) }
                }
            }
            RichMessage(text: entry.text, sessions: entry.sessions, font: Self.font, selectable: true)
            if !entry.sessions.isEmpty {
                FlowLayout(spacing: 8) {
                    ForEach(entry.sessions, id: \.self) { address in
                        SessionChip(who: who(address)) { actions.focusSession(address) }
                    }
                }
            }
        }
    }

    /// A session's update: its header (with the terminal button), its words, and Reply…,
    /// which points the box at the session.
    private var session: some View {
        VStack(alignment: .leading, spacing: 4) {
            ChatWhoHeader(who: actions.liveWho(entry), terminal: ChatThreadModel.terminalTarget(for: entry), onTerminal: actions.focus,
                          stop: actions.stopTarget(entry), onStop: actions.stop)
            RichMessage(text: entry.text, sessions: entry.sessions, font: Self.font, selectable: true)
            if ChatThreadModel.offersSessionReply(entry) {
                Button("Reply…") { actions.reply(entry) }
                    .buttonStyle(.plain)
                    .font(.system(size: 13))
                    .foregroundStyle(ChatColors.link)
                    .help("Message this session")
                    .accessibilityIdentifier("chat.entry.reply")
            }
        }
    }

    /// An answered card, shrunk to one line: "api-server · You allowed git push · 2:14 PM".
    private var outcome: some View {
        HStack(spacing: 6) {
            SessionDot(color: entry.who.color, shown: entry.who.kind == "session")
            (Text(ChatThreadModel.displayName(entry.who)).fontWeight(.semibold).foregroundColor(.primary)
                + Text(" · \(entry.request?.outcome ?? "") · \(ChatTime.short(entry.at))"))
                .lineLimit(2)
        }
        .font(.system(size: 13))
        .foregroundStyle(ChatColors.label)
    }
}

/// A coloured dot, its name in bold, then "· your session · working" (`ChatThreadModel.header`).
/// Words still coming (`chat.live`), never stored: yours as a lighter bubble on the right,
/// Pixie's under its name in the secondary colour. The final entry takes their place.
struct ChatLiveView: View {
    let line: ChatLive

    var body: some View {
        Group {
            if line.who == "you" {
                Text(line.text)
                    .font(.system(size: 14))
                    .lineSpacing(2)
                    .foregroundStyle(.primary)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 10)
                    .background(UnevenRoundedRectangle(topLeadingRadius: 18, bottomLeadingRadius: 18,
                                                       bottomTrailingRadius: 4, topTrailingRadius: 18)
                        .fill(ChatColors.liveBubble))
                    .padding(.leading, 120)
            } else {
                VStack(alignment: .leading, spacing: 4) {
                    Text(ChatThreadModel.header(ChatWho(kind: line.who)))
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(ChatColors.label)
                    Text(line.text)
                        .font(.system(size: 14))
                        .lineSpacing(2)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: line.who == "you" ? .trailing : .leading)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("chat.live.\(line.who)")
    }
}

/// The dot and the name, then the terminal button when there is one, the stop button while it
/// works, then the rest.
struct ChatWhoHeader: View {
    let who: ChatWho
    var size: CGFloat = 12
    var terminal: ChatTerminalTarget?
    var onTerminal: (ChatTerminalTarget) -> Void = { _ in }
    var stop: ChatStopTarget?
    var onStop: (ChatStopTarget) -> Void = { _ in }

    var body: some View {
        let name = ChatThreadModel.displayName(who)
        let rest = String(ChatThreadModel.header(who).dropFirst(name.count))
        HStack(spacing: 4) {
            HStack(spacing: 6) {
                SessionDot(color: who.color, shown: who.kind == "session")
                Text(name).fontWeight(.semibold).foregroundColor(.primary)
                    .lineLimit(1)
            }
            if let terminal {
                TerminalButton(target: terminal) { onTerminal(terminal) }
            }
            if let stop {
                StopButton(target: stop) { onStop(stop) }
            }
            Text(rest.trimmingCharacters(in: .whitespaces))
                .lineLimit(1)
                .truncationMode(.tail)
        }
        .font(.system(size: size))
        .foregroundStyle(ChatColors.label)
        .accessibilityElement(children: .contain)
    }
}

/// The small terminal button right after a responder's name: brings its tab or pane forward.
struct TerminalButton: View {
    let target: ChatTerminalTarget
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "terminal")
                .font(.system(size: 11, weight: .medium))
                .foregroundStyle(ChatColors.link)
                .frame(width: 18, height: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .help(target.label)
        .accessibilityLabel(target.label)
        .accessibilityIdentifier("chat.entry.terminal")
    }
}

/// The small square stop button after the terminal button, while the responder works (#309):
/// interrupts its turn, as Esc in its terminal does. No confirmation.
struct StopButton: View {
    let target: ChatStopTarget
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Image(systemName: "stop.fill")
                .font(.system(size: 9, weight: .medium))
                .foregroundStyle(ChatColors.link)
                .frame(width: 18, height: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .disabled(target.stopping) // its call is on its way: one Esc per click (#309)
        .help(target.label)
        .accessibilityLabel(target.label)
        .accessibilityIdentifier("chat.entry.stop")
    }
}

struct SessionDot: View {
    let color: Int
    var shown = true
    var size: CGFloat = 8

    var body: some View {
        if shown {
            Circle()
                .fill(ChatColors.session(color))
                .frame(width: size, height: size)
                .accessibilityHidden(true)
        }
    }
}

/// A session a reply mentions: its dot and name; a click shows its terminal.
private struct SessionChip: View {
    let who: ChatWho
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                SessionDot(color: who.color)
                Text("\(ChatThreadModel.displayName(who)) ›")
                    .lineLimit(1)
            }
            .font(.system(size: 12))
            .foregroundStyle(.primary)
            .padding(.horizontal, 10)
            .padding(.vertical, 4)
            .overlay(Capsule().strokeBorder(ChatColors.border))
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help("Go to \(ChatThreadModel.displayName(who))")
        .accessibilityLabel("Go to \(ChatThreadModel.displayName(who))")
    }
}

/// The files a message of yours went with: icon and name, nothing to remove.
private struct SentAttachments: View {
    let paths: [String]

    var body: some View {
        FlowLayout(spacing: 4) {
            ForEach(paths, id: \.self) { path in
                HStack(spacing: 4) {
                    Image(nsImage: NSWorkspace.shared.icon(forFile: path))
                        .resizable()
                        .frame(width: 16, height: 16)
                    Text((path as NSString).lastPathComponent)
                        .font(.caption)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: 160, alignment: .leading)
                }
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(RoundedRectangle(cornerRadius: 6).fill(.quaternary))
            }
        }
        .frame(maxWidth: 360, alignment: .trailing)
    }
}

/// Times as the thread shows them.
enum ChatTime {
    private static let time: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .none
        f.timeStyle = .short
        return f
    }()

    private static let divider: DateFormatter = {
        let f = DateFormatter()
        f.dateStyle = .medium
        f.timeStyle = .short
        f.doesRelativeDateFormatting = true
        return f
    }()

    static func date(_ at: Int64) -> Date { Date(timeIntervalSince1970: TimeInterval(at) / 1000) }

    /// "2:14 PM".
    static func short(_ at: Int64) -> String { time.string(from: date(at)) }

    /// "Today at 2:10 PM", above messages more than 5 minutes after the last.
    static func divider(_ at: Int64) -> String { divider.string(from: date(at)) }
}
