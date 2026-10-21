import DeskPixieCore
import SwiftUI

/// The roster chip under Pixie and, when open, the list (spec docs/superpowers/specs/2026-10-15-agent-roster-design.md
/// §3). Drawn by `AgentRosterController` from its model; `below` puts the list under the chip.
struct AgentRosterView: View {
    let model: AgentRosterModel
    let below: Bool
    /// The chip is never wider than Pixie (it must not reach the updates bubble at Pixie's side).
    let maxChipWidth: CGFloat
    let onChip: () -> Void
    let onRow: (RosterRow) -> Void
    /// The text box and its toggle (#221).
    let chat: QuickChatModel
    let chatState: QuickChatState
    /// The card on its own, without the input line (#224): the controller decides.
    let captionCard: Bool
    /// "2m ago" for the card's header.
    let ago: String?
    let onMode: (String) -> Void
    let onSend: () -> Void
    let onStop: () -> Void
    let onCloseChat: () -> Void

    static let listWidth: CGFloat = 260

    var body: some View {
        VStack(spacing: 4) {
            if model.open && !below { list }
            if showsCard && !below { card }
            HStack(spacing: 6) {
                if model.visible {
                    chip.background(GeometryReader { g in
                        Color.clear.preference(key: ChipFrameKey.self, value: g.frame(in: .global))
                    })
                }
                ReplyModeToggle(mode: chat.mode, onPick: onMode)
            }
            if model.open && below { list }
            if showsCard && below { card }
        }
        .fixedSize()
    }

    private var chatOpen: Bool { chat.open && chat.mode == "text" }
    /// The text box when open, or the card alone (a caption, or the last words, #224).
    private var showsCard: Bool { chatOpen || captionCard }

    private var card: some View {
        QuickChatCard(model: chat, state: chatState, showsInput: chatOpen, ago: ago,
                      onSend: onSend, onStop: onStop, onClose: onCloseChat)
    }

    private var chip: some View {
        Button(action: onChip) {
            // SF Symbols, not the ⚙/⌨ characters: those render tiny at small text sizes.
            HStack(spacing: 10) {
                if model.view.agentCount > 0 { count("gearshape.fill", model.view.agentCount) }
                if model.view.yourCount > 0 { count("keyboard", model.view.yourCount) }
            }
                .font(.title3.weight(.semibold))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                .background(Capsule().fill(.regularMaterial))
                .overlay(Capsule().strokeBorder(.separator))
        }
        .buttonStyle(.plain)
        .frame(maxWidth: maxChipWidth)
        .accessibilityLabel("\(model.view.agentCount) of Pixie's agents, \(model.view.yourCount) of your sessions")
    }

    private func count(_ symbol: String, _ n: Int) -> some View {
        HStack(spacing: 4) {
            Image(systemName: symbol)
            Text("\(n)")
        }
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(model.sections, id: \.title) { section in
                Text(section.title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.top, 4)
                ForEach(section.rows) { r in
                    let clickable = AgentRosterModel.clickable(r)
                    Button { onRow(r) } label: { row(r) }
                        .buttonStyle(.plain)
                        .disabled(!clickable)
                        .onHover { inside in
                            // A pointing hand on clickable rows only (spec §4).
                            guard clickable else { return }
                            if inside { NSCursor.pointingHand.push() } else { NSCursor.pop() }
                        }
                }
                if section.more > 0 {
                    Text("+\(section.more) more")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                }
            }
        }
        .padding(6)
        .frame(width: Self.listWidth, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }

    private func row(_ r: RosterRow) -> some View {
        HStack(spacing: 6) {
            Text(AgentRosterModel.dot(r.status))
                .foregroundStyle(r.status == "working" ? .green : r.status == "blocked" ? .orange : .secondary)
            Text(r.label).font(.callout).lineLimit(1).truncationMode(.tail)
            Spacer(minLength: 4)
            Text(r.status).font(.caption).foregroundStyle(.secondary)
            if let d = r.detail, !d.isEmpty {
                Text(d).font(.caption).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .opacity(r.reachable ? 1 : 0.45)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}

/// Where the session chip is in the window (top-left origin): hovering it opens the list.
struct ChipFrameKey: PreferenceKey {
    static let defaultValue: CGRect = .zero
    static func reduce(value: inout CGRect, nextValue: () -> CGRect) { value = nextValue() }
}
