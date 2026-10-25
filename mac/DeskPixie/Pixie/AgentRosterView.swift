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
    /// The stop button on a working row (#309).
    var onStopRow: (RosterRow) -> Void = { _ in }
    /// The text box and its toggle (#221).
    let chat: QuickChatModel
    let chatState: QuickChatState
    /// The card on its own, without the input line (#224): the controller decides.
    let captionCard: Bool
    /// The other side's last words above the caption, and how far they've faded (#254).
    let earlier: QuickChatModel.Earlier?
    let earlierPhase: QuickChatModel.EarlierPhase
    let onMode: (String) -> Void
    let onSend: () -> Void
    let onStop: () -> Void
    let onCloseChat: () -> Void
    let onResize: () -> Void
    let onDismiss: () -> Void
    /// The card's arrows through Pixie's recent replies (#263).
    var onStep: (ReplyStep) -> Void = { _ in }
    let attachments: AttachmentUI
    /// The caption's buttons to sessions (#261), and what a click does.
    /// A scroll to another page of the reply deck (#286).
    var onPage: (Int) -> Void = { _ in }
    var onScroll: (Bool) -> Void = { _ in }
    var onSession: (String) -> Void = { _ in }
    /// Pixie's "Want me to take this?" for a new copy (#240), nil when not up.
    let offer: ClipboardKind?
    let onOffer: () -> Void
    /// The round button that shows and hides the chat window (#302).
    let chatWindow: ChatWindowPresence
    let onChatWindow: () -> Void

    static let listWidth: CGFloat = 260

    var body: some View {
        VStack(spacing: 4) {
            if model.open && !below { list }
            if showsCard && !below { earlierBubble; card }
            if let offer, !below { offerBubble(offer) }
            HStack(spacing: 6) {
                if model.visible {
                    chip.background(GeometryReader { g in
                        Color.clear.preference(key: ChipFrameKey.self, value: g.frame(in: .global))
                    })
                }
                ReplyModeToggle(mode: chat.mode, onPick: onMode)
                // A window, not a third mode: set apart from the toggle.
                Rectangle().fill(.separator).frame(width: 1, height: 22)
                ChatWindowButton(toggle: chatWindow.toggle, onClick: onChatWindow)
            }
            // Its own width, not the card's: a wider card would stretch the chip's frame up to
            // maxChipWidth and open a gap before the toggle (#227).
            .fixedSize()
            if model.open && below { list }
            if let offer, below { offerBubble(offer) }
            if showsCard && below { earlierBubble; card }
        }
        .fixedSize()
    }

    private var chatOpen: Bool { chat.open && chat.mode == "text" }
    /// The text box when open, or the card alone (a caption, or the last words, #224).
    private var showsCard: Bool { chatOpen || captionCard }

    private var card: some View {
        QuickChatCard(model: chat, state: chatState, showsInput: chatOpen,
                      onSend: onSend, onStop: onStop, onClose: onCloseChat, onResize: onResize, onDismiss: onDismiss,
                      onStep: onStep, above: !below, onPage: onPage, onScroll: onScroll, attachments: attachments,
                      rows: { [view = model.view] in AgentRosterModel.buttons(for: $0, in: view) }, onSession: onSession)
    }

    /// The other side's last words, above the caption until they fade (#254).
    @ViewBuilder private var earlierBubble: some View {
        if let earlier, earlierPhase != .gone, !chatOpen {
            VStack(alignment: .leading, spacing: 2) {
                Text(earlier.isYou ? "You" : "Pixie")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                // Your words or Pixie's, with their links and files clickable (#277).
                RichMessage(text: earlier.text, maxLines: 3)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)
            .frame(width: QuickChatCard.width, alignment: .leading)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
            .opacity(earlierPhase == .fading ? 0 : 1)
            .animation(.easeOut(duration: QuickChatModel.fadeDuration), value: earlierPhase)
        }
    }

    private func offerBubble(_ kind: ClipboardKind) -> some View {
        Button(action: onOffer) {
            HStack(spacing: 6) {
                Image(systemName: kind == .files ? "doc.fill" : "photo.fill")
                Text("Want me to take this?")
            }
            .font(.callout.weight(.semibold))
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .background(Capsule().fill(.regularMaterial))
            .overlay(Capsule().strokeBorder(.separator))
        }
        .buttonStyle(.plain)
        .help("Attach what you copied to your next request")
        .transition(.opacity)
    }

    private var chip: some View {
        Button(action: onChip) {
            // SF Symbols, not the ⚙/⌨ characters: those render tiny at small text sizes.
            HStack(spacing: 10) {
                if model.busy { BusyDot() }
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
        .accessibilityLabel("\(model.view.agentCount) of Pixie's agents, \(model.view.yourCount) of your sessions"
                            + (model.busy ? ", working" : ""))
        .accessibilityIdentifier("roster.chip")
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
                ForEach(section.rows) { r in row(r) }
                if section.more > 0 {
                    Text("+\(section.more) more")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                }
            }
            if let notice = model.notice {
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 8)
                    .padding(.top, 4)
                    .accessibilityIdentifier("roster.notice")
            }
        }
        .padding(6)
        .frame(width: Self.listWidth, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }

    /// A row: a click brings its agent forward; while it works, a stop button sits before its
    /// detail ("terminal", its repo) and interrupts its turn (#309). Two buttons side by side,
    /// not one inside the other, so each gets its own clicks.
    private func row(_ r: RosterRow) -> some View {
        let clickable = AgentRosterModel.clickable(r)
        return HStack(spacing: 6) {
            Button { onRow(r) } label: {
                HStack(spacing: 6) {
                    Text(AgentRosterModel.dot(r.status))
                        .foregroundStyle(r.status == "working" ? .green : r.status == "blocked" ? .orange : .secondary)
                    Text(r.label).font(.callout).lineLimit(1).truncationMode(.tail)
                    Spacer(minLength: 4)
                    Text(r.status).font(.caption).foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!clickable)
            .onHover { inside in
                // A pointing hand on clickable rows only (spec §4).
                guard clickable else { return }
                if inside { NSCursor.pointingHand.push() } else { NSCursor.pop() }
            }
            .accessibilityIdentifier("roster.row")
            if AgentRosterModel.showsStop(r) {
                Button { onStopRow(r) } label: {
                    Image(systemName: "stop.fill")
                        .font(.system(size: 8, weight: .medium))
                        .foregroundStyle(.secondary)
                        .frame(width: 16, height: 14)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.borderless)
                .help(AgentRosterModel.stopLabel(r))
                .accessibilityLabel(AgentRosterModel.stopLabel(r))
                .accessibilityIdentifier("roster.stop")
            }
            if let d = r.detail, !d.isEmpty {
                Text(d).font(.caption).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .opacity(r.reachable ? 1 : 0.45)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
    }
}

/// The chip's busy dot (#234), as on the landing page: 7 pt, green with a soft glow, its
/// opacity 1 → 0.4 → 1 over 1.6 s; still with Reduce Motion.
private struct BusyDot: View {
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var dim = false

    var body: some View {
        Circle()
            .fill(Color.green)
            .frame(width: 7, height: 7)
            .shadow(color: .green, radius: 3)
            .opacity(dim ? 0.4 : 1)
            // Only the opacity animates, never the chip's layout around it.
            .animation(reduceMotion ? nil : .easeInOut(duration: 0.8).repeatForever(autoreverses: true), value: dim)
            .onAppear { dim = !reduceMotion }
            // A Reduce Motion change rebuilds the dot: a running repeat can't be stopped in place.
            .id(reduceMotion)
    }
}

/// Where the session chip is in the window (top-left origin): hovering it opens the list.
struct ChipFrameKey: PreferenceKey {
    static let defaultValue: CGRect = .zero
    static func reduce(value: inout CGRect, nextValue: () -> CGRect) { value = nextValue() }
}
