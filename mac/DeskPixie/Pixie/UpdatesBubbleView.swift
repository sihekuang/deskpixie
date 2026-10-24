import AppKit
import DeskPixieCore
import SwiftUI

/// The updates bubble's content (spec §3, #286): a count badge, or the badge deck: one card at a
/// time for what waits on you, oldest first, with ‹ i of n › and the edge of the next one behind.
/// A click on the card brings up the line's pane; a two-finger swipe steps like the arrows.
/// Drawn by `UpdatesBubbleController` from its model.
struct UpdatesBubbleView: View {
    let count: Int
    let phase: UpdatesBubbleModel.Phase
    /// The deck's cards (oldest first), the one shown, and where it is ("1 of 1" alone).
    let lines: [UpdateLine]
    let current: UpdateLine?
    let position: (index: Int, count: Int)?
    /// The deck keeps one width as you step through it.
    let width: CGFloat
    let onLine: (UpdateLine) -> Void
    /// The arrows: +1 newer, −1 older.
    let onStep: (Int) -> Void
    /// A scroll to another card, by its id.
    var onSelect: (UInt64) -> Void = { _ in }
    /// The deck changed height: the panel is sized from the view.
    var onResize: () -> Void = {}
    /// Allow or Deny on a permission question (token, allow). The controller ignores a tap that
    /// comes too soon after the buttons appear (`UpdatesBubbleModel.acceptsDecision`).
    let onDecide: (String, Bool) -> Void
    /// A session menu's option (token, option from 1), under the same stray-click guard (#105).
    let onChoose: (String, Int) -> Void
    /// A click on the badge (opens the bubble, like hovering).
    let onBadge: () -> Void
    /// The menu whose answer is being typed (#268), and its draft.
    var typing: String? = nil
    @ObservedObject var typed: BubbleTyping
    /// "Type something…" (token), sending the typed answer (token), putting the field away, and
    /// "Chat about this" (token) on a menu held in its session's hook (#268).
    var onStartTyping: (String) -> Void = { _ in }
    var onSendTyped: (String) -> Void = { _ in }
    var onStopTyping: () -> Void = {}
    var onChat: (String) -> Void = { _ in }

    /// The bubble sizes to its lines (UpdatesBubbleLayout, #105, #278).
    static func width(for lines: [UpdateLine]) -> CGFloat { UpdatesBubbleLayout.width(for: lines) }

    var body: some View {
        switch phase {
        case .hidden:
            EmptyView()
        case .badge:
            badge
        case .popped, .expanded:
            bubble
        }
    }

    private var badge: some View {
        Button(action: onBadge) {
            Text(count > 9 ? "9+" : "\(count)")
                .font(.caption.weight(.semibold))
                .monospacedDigit()
                .foregroundStyle(.white)
                .padding(.horizontal, 7)
                .padding(.vertical, 3)
                .background(Capsule().fill(Color.accentColor))
        }
        .buttonStyle(.plain)
        .padding(2)
        .fixedSize()
    }

    @ViewBuilder private var bubble: some View {
        if let line = current {
            VStack(alignment: .leading, spacing: 2) {
                header(line)
                // The cards side by side (#286): a two-finger swipe or the arrows page them.
                DeckPager(ids: lines.map(\.id), selected: line.id, width: width - 12, anchor: .leading,
                          onSelect: onSelect, onResize: onResize) { id in
                    if let l = lines.first(where: { $0.id == id }) { card(l) }
                }
            }
            .padding(6)
            .frame(width: width, alignment: .leading)
            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
            .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// "Permission · coordinator" in amber with a lock, or "<session> is asking"; then ‹ i of n ›.
    private func header(_ line: UpdateLine) -> some View {
        HStack(spacing: 4) {
            if line.offersDecision {
                Image(systemName: "lock.fill")
            } else if line.kind == "question" || line.asking == true {
                Image(systemName: "questionmark.circle")
            }
            Text(line.deckHeader)
                .lineLimit(1)
                .truncationMode(.tail)
            Spacer(minLength: 4)
            if let position {
                deckArrow("chevron.left", "Previous", enabled: position.index > 1) { onStep(-1) }
                Text("\(position.index) of \(position.count)")
                    .monospacedDigit()
                    .foregroundStyle(.secondary)
                    .accessibilityLabel("\(position.index) of \(position.count)")
                deckArrow("chevron.right", "Next", enabled: position.index < position.count) { onStep(1) }
            }
        }
        .font(.caption.weight(.semibold))
        .foregroundStyle(line.offersDecision ? Color.orange : Color.secondary)
        .padding(.horizontal, 8)
        .padding(.top, 4)
    }

    private func deckArrow(_ symbol: String, _ label: String, enabled: Bool, _ action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: symbol)
                .font(.caption2.weight(.bold))
                .frame(width: 16, height: 16)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .foregroundStyle(enabled ? .primary : .tertiary)
        .disabled(!enabled)
        .help(label)
        .accessibilityLabel(label)
    }

    private func card(_ line: UpdateLine) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Button { if !RichTextClicks.takeSpanPress() { onLine(line) } } label: { row(line) }
                .buttonStyle(.plain)
            if line.offersDecision, let token = line.token {
                HStack(spacing: 6) {
                    Button("Allow") { onDecide(token, true) }
                        .buttonStyle(.borderedProminent)
                    Button("Deny") { onDecide(token, false) }
                        .buttonStyle(.bordered)
                }
                .controlSize(.small)
                .padding(.horizontal, 8)
                .padding(.bottom, 4)
            }
            if line.offersOptions, let token = line.token, let options = line.options {
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(options.enumerated()), id: \.offset) { i, option in
                        Button { onChoose(token, i + 1) } label: {
                            VStack(alignment: .leading, spacing: 1) {
                                Text(option)
                                if let detail = line.detail(of: i) {
                                    Text(detail)
                                        .font(.caption)
                                        .opacity(0.75)
                                }
                            }
                            .multilineTextAlignment(.leading)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .buttonStyle(OptionButtonStyle(prominent: i == 0 && typing != token))
                        .disabled(typing == token)
                    }
                    if line.offersTyped {
                        Divider().padding(.vertical, 2)
                        if typing == token {
                            typedField(token)
                        } else {
                            Button { onStartTyping(token) } label: {
                                Label("Type something…", systemImage: "pencil")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .buttonStyle(OutlineButtonStyle())
                            Button { onChat(token) } label: {
                                Label("Chat about this", systemImage: "bubble.left")
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .buttonStyle(OutlineButtonStyle())
                        }
                    }
                }
                .controlSize(.small)
                .padding(.horizontal, 8)
                .padding(.bottom, 4)
            }
            if line.offersTerminal {
                // Asks without buttons: the answer goes in its terminal.
                Button { onLine(line) } label: {
                    Label("Open its terminal", systemImage: "terminal")
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .buttonStyle(OptionButtonStyle(prominent: true))
                .padding(.horizontal, 8)
                .padding(.bottom, 4)
            }
        }
        .padding(.vertical, 2)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.primary.opacity(0.05)))
    }

    /// The typed answer's field (#268): Return sends, Esc goes back to the options.
    private func typedField(_ token: String) -> some View {
        let canSend = !typed.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        return VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                TextField("Your answer", text: $typed.draft, axis: .vertical)
                    .textFieldStyle(.plain)
                    .font(.system(size: NSFont.smallSystemFontSize))
                    .lineLimit(1...4)
                    .fixedSize(horizontal: false, vertical: true)
                    .focused($fieldFocused)
                    .onSubmit { onSendTyped(token) }
                    .onExitCommand(perform: onStopTyping)
                    // With text in the field, Esc is the field editor's "complete": take it first.
                    .onKeyPress(.escape) {
                        onStopTyping()
                        return .handled
                    }
                    .padding(.horizontal, 6)
                    .padding(.vertical, 4)
                    .background(RoundedRectangle(cornerRadius: 6).strokeBorder(Color.accentColor, lineWidth: 1.5))
                Button { onSendTyped(token) } label: {
                    Image(systemName: "arrow.up.circle.fill")
                        .font(.title3)
                        .foregroundStyle(canSend ? Color.accentColor : Color.secondary)
                }
                .buttonStyle(.plain)
                .disabled(!canSend)
                .help("Send")
                .accessibilityLabel("Send")
            }
            Text("Return sends · Esc goes back")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
        .onAppear { fieldFocused = true }
        .onChange(of: typed.focusRequests) { fieldFocused = true }
    }

    @FocusState private var fieldFocused: Bool

    private func row(_ line: UpdateLine) -> some View {
        VStack(alignment: .leading, spacing: 1) {
            // Links, files and code can be clicked (#277); a question is read in full.
            RichMessage(text: line.shownText, sessions: line.session.map { [$0] } ?? [],
                        maxLines: line.kind == "question" ? 0 : UpdatesBubbleLayout.maxLines)
                .fixedSize(horizontal: false, vertical: true)
            if let note = line.note, !note.isEmpty {
                Text(note)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .lineLimit(6)
                    .truncationMode(.tail)
                    .fixedSize(horizontal: false, vertical: true)
            }
            TimelineView(.periodic(from: .now, by: 30)) { context in
                Text(updateAge(seconds: context.date.timeIntervalSince1970 - Double(line.at) / 1000))
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .contentShape(Rectangle())
    }
}

/// "Type something…" and "Chat about this" (#268): outlined, so they don't read as one of the
/// session's own options.
private struct OutlineButtonStyle: ButtonStyle {
    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: NSFont.smallSystemFontSize))
            .foregroundStyle(Color.primary)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(Color.primary.opacity(0.25)))
            .opacity(configuration.isPressed ? 0.7 : 1)
            .contentShape(Rectangle())
    }
}

/// The typed answer's draft (#268), kept outside the view so redraws don't lose it.
@MainActor
final class BubbleTyping: ObservableObject {
    @Published var draft = ""
    /// Bumped to put the cursor in the field again.
    @Published var focusRequests = 0
}

/// A session option's button: full width, its label wrapping (AppKit's bordered buttons keep to
/// one line); the first option filled like Allow.
private struct OptionButtonStyle: ButtonStyle {
    let prominent: Bool

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.system(size: NSFont.smallSystemFontSize))
            .foregroundStyle(prominent ? Color.white : Color.primary)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(
                RoundedRectangle(cornerRadius: 6)
                    .fill(prominent ? Color.accentColor : Color.primary.opacity(0.08)))
            .opacity(configuration.isPressed ? 0.7 : 1)
            .contentShape(Rectangle())
    }
}
