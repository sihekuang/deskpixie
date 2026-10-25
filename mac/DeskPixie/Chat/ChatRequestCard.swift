import DeskPixieCore
import SwiftUI

/// A request waiting on you (spec §4.1): an orange-edged card under its session's header. A
/// permission has Allow and Deny (`agent.decide`); a question its options (`agent.choose`) and,
/// when it takes a typed answer or has no buttons, Reply…, which points the box at it. Once
/// answered, the thread shows its outcome line instead.
struct ChatRequestCard: View {
    let entry: ChatEntry
    let actions: ChatActions

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ChatWhoHeader(who: actions.liveWho(entry), terminal: ChatThreadModel.terminalTarget(for: entry), onTerminal: actions.focus,
                          stop: actions.stopTarget(entry), onStop: actions.stop)
            if entry.request?.kind == "permission" {
                HStack(alignment: .firstTextBaseline, spacing: 8) {
                    Image(systemName: "lock")
                        .font(.system(size: 12, weight: .semibold))
                        .foregroundStyle(ChatColors.needsTitle)
                        .accessibilityHidden(true)
                    RichMessage(text: entry.text, sessions: entry.sessions, font: ChatEntryView.font, selectable: true)
                }
            } else {
                RichMessage(text: entry.text, sessions: entry.sessions, font: ChatEntryView.font, selectable: true)
            }
            buttons
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 12).fill(ChatColors.needsCard))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(ChatColors.needsEdge))
    }

    private var token: String { entry.request?.token ?? "" }

    private var buttons: some View {
        FlowLayout(spacing: 8) {
            if ChatThreadModel.offersDecision(entry) {
                Button("Allow") { actions.decide(token, true) }
                    .buttonStyle(CardButtonStyle(prominent: true))
                    .accessibilityIdentifier("chat.request.allow")
                Button("Deny") { actions.decide(token, false) }
                    .buttonStyle(CardButtonStyle())
                    .accessibilityIdentifier("chat.request.deny")
            }
            if ChatThreadModel.offersOptions(entry) {
                ForEach(Array((entry.request?.options ?? []).enumerated()), id: \.offset) { i, option in
                    Button(option) { actions.choose(token, i + 1) }
                        .buttonStyle(CardButtonStyle())
                }
            }
            if ChatThreadModel.offersReply(entry) {
                Button("Reply…") { actions.reply(entry) }
                    .buttonStyle(CardButtonStyle(tint: ChatColors.link))
                    .accessibilityIdentifier("chat.request.reply")
                    .help("Answer in the message box")
            }
        }
    }
}

/// The card's buttons, as in the mockup: 32 pt tall, Allow filled blue, the others outlined.
/// AppKit's bordered buttons keep to one short line; an option's label may be long.
private struct CardButtonStyle: ButtonStyle {
    var prominent = false
    var tint: Color = .primary

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: 13, weight: prominent ? .semibold : .regular))
            .foregroundStyle(prominent ? Color.white : tint)
            .multilineTextAlignment(.leading)
            .padding(.horizontal, prominent ? 16 : 14)
            .padding(.vertical, 6)
            .frame(minHeight: 32)
            .background(RoundedRectangle(cornerRadius: 8).fill(prominent ? ChatColors.bubble : ChatColors.pill))
            .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(prominent ? Color.clear : ChatColors.border))
            .opacity(configuration.isPressed ? 0.7 : 1)
            .contentShape(RoundedRectangle(cornerRadius: 8))
    }
}
