import DeskPixieCore
import SwiftUI

/// What the text box keeps across redraws (#221): the draft and a request to focus the field.
/// The roster's root view is rebuilt on every change; these must survive that.
@MainActor
@Observable
final class QuickChatState {
    var draft = ""
    /// Bumped to put the cursor in the field (the hotkey in text mode, opening the box).
    var focusRequests = 0
}

/// The 🔊/💬 toggle beside the session count: one capsule drawn like the count chip.
struct ReplyModeToggle: View {
    let mode: String
    let onPick: (String) -> Void

    var body: some View {
        HStack(spacing: 2) {
            segment("voice", "speaker.wave.2.fill", "Voice: talk to Pixie")
            segment("text", "bubble.left.fill", "Text: type a quick message")
        }
        .padding(3)
        .background(Capsule().fill(.regularMaterial))
        .overlay(Capsule().strokeBorder(.separator))
    }

    private func segment(_ value: String, _ symbol: String, _ help: String) -> some View {
        Button { onPick(value) } label: {
            Image(systemName: symbol)
                .font(.title3.weight(.semibold))
                .foregroundStyle(mode == value ? .primary : .secondary)
                .frame(width: 34, height: 26)
                .background(Capsule().fill(mode == value ? AnyShapeStyle(.background) : AnyShapeStyle(.clear)))
                .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(help)
        .accessibilityAddTraits(mode == value ? .isSelected : [])
    }
}

/// The text box (text mode): the coordinator's spoken answer, Stop while it works, and one line
/// to type in. Built like the session list (spec §5). In voice mode it is the caption alone
/// (#224): what Pixie said, with no input line.
struct QuickChatCard: View {
    let model: QuickChatModel
    @Bindable var state: QuickChatState
    let showsInput: Bool
    let ago: String?
    let onSend: () -> Void
    let onStop: () -> Void
    let onClose: () -> Void
    let onResize: () -> Void
    let onDismiss: () -> Void
    var onStep: (ReplyStep) -> Void = { _ in }
    /// The chips, the hint row, 📎 and drops (#240).
    let attachments: AttachmentUI
    /// Buttons to the sessions the words are about (#261), and what a click does.
    var sessions: [RosterRow] = []
    var onSession: (String) -> Void = { _ in }

    static let width: CGFloat = 260
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !model.shownText.isEmpty || model.phase != .ready || model.showsEarlierOnly {
                HStack(spacing: 4) {
                    let who = model.showsEarlierOnly ? "Earlier replies" : model.isYou && model.viewed == nil ? "You" : "Pixie"
                    Text(ago.map { "\(who) · \($0)" } ?? who)
                    Spacer(minLength: 4)
                    if model.position != nil || model.showsEarlierOnly {
                        ReplyArrows(model: model, position: model.position, onStep: onStep)
                    }
                    if model.canDismiss {
                        // Puts the words away (#224); the setting does it on its own after a while.
                        Button(action: onDismiss) {
                            Image(systemName: "xmark")
                                .font(.caption2.weight(.bold))
                                .frame(width: 16, height: 16)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .help("Dismiss")
                        .accessibilityLabel("Dismiss")
                    }
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .padding(.horizontal, 8)
                .padding(.top, 4)
                if !model.shownText.isEmpty {
                    ScrollView {
                        Text(model.shownText)
                            .font(.callout)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 180)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 8)
                }
                if !sessions.isEmpty {
                    SessionButtons(sessions: sessions, onSession: onSession)
                        .padding(.horizontal, 8)
                        .padding(.top, 2)
                }
                if model.phase == .stopped && model.viewed == nil {
                    Text("Stopped.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                }
                if let status = model.statusText {
                    HStack {
                        Text(status)
                        Spacer(minLength: 4)
                        if model.showsStop {
                            Button(action: onStop) {
                                Label("Stop", systemImage: "stop.fill")
                                    .labelStyle(.titleAndIcon)
                            }
                            .buttonStyle(.plain)
                            .help("Stop the coordinator")
                        }
                    }
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                }
                if showsInput || attachments.model.chipsVisible { Divider().padding(.top, 2) }
            }
            if attachments.model.chipsVisible {
                AttachmentChips(items: attachments.model.items, onRemove: attachments.onRemove)
                    .padding(.horizontal, 8)
                    .padding(.top, 2)
            }
            if let notice = attachments.model.notice {
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
            } else if showsInput, let hint = attachments.hint {
                HStack(spacing: 4) {
                    Image(systemName: "doc.on.clipboard")
                    Text(hint)
                    Text("·")
                    Button("Attach", action: attachments.onAttachHint)
                        .buttonStyle(.link)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 8)
            }
            if showsInput {
                HStack(alignment: .bottom, spacing: 6) {
                    Button(action: attachments.onPick) {
                        Image(systemName: "paperclip")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                    .help("Attach files")
                    .accessibilityLabel("Attach files")
                    // Grows with what is typed, up to a few lines, then scrolls; Return sends.
                    TextField("Message Pixie", text: $state.draft, axis: .vertical)
                        .textFieldStyle(.plain)
                        .font(.callout)
                        .lineLimit(1...5)
                        .fixedSize(horizontal: false, vertical: true)
                        .focused($focused)
                        .onSubmit(onSend)
                        .onExitCommand(perform: onClose)
                        // With text in the field, Esc is the field editor's "complete" and never
                        // reaches onExitCommand: take it first.
                        .onKeyPress(.escape) {
                            onClose()
                            return .handled
                        }
                    Button(action: onSend) {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title3)
                            .foregroundStyle(canSend ? .primary : .tertiary)
                    }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .help("Send")
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 6)
            }
        }
        .padding(6)
        .frame(width: Self.width, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
        .onDrop(of: [.fileURL, .image], isTargeted: nil, perform: attachments.onDrop)
        .onAppear { if showsInput { focused = true } }
        .onChange(of: state.focusRequests) { focused = true }
        // The panel is sized from the view: a taller field needs a new frame.
        .onChange(of: state.draft) { onResize() }
        .onChange(of: attachments.model) { onResize() }
    }

    private var canSend: Bool { model.canSend(state.draft, attachments: attachments.model.items.count) }
}

/// What the card's arrows ask for (#263).
enum ReplyStep { case back, forward, newest }

/// ‹ 2/3 › on the card (#263): step through Pixie's recent replies. While you look back a new
/// reply leaves you there and » goes to it. With nothing current, only ‹.
struct ReplyArrows: View {
    let model: QuickChatModel
    /// Nil with nothing current and not looking back: only ‹ to the kept replies.
    let position: QuickChatModel.Position?
    let onStep: (ReplyStep) -> Void

    var body: some View {
        HStack(spacing: 2) {
            arrow("chevron.left", "Earlier reply", enabled: model.canBack) { onStep(.back) }
            if let position {
                Text("\(position.index)/\(position.count)")
                    .monospacedDigit()
                    .frame(minWidth: 26)
                    .accessibilityLabel("Reply \(position.index) of \(position.count)")
                arrow("chevron.right", "Later reply", enabled: model.canForward) { onStep(.forward) }
            }
            if model.newerWaiting {
                Button { onStep(.newest) } label: {
                    Image(systemName: "chevron.right.2")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(.white)
                        .padding(.horizontal, 4)
                        .frame(height: 16)
                        .background(Capsule().fill(Color.accentColor))
                        .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .help("Newest reply")
                .accessibilityLabel("Newest reply")
            }
        }
    }

    private func arrow(_ symbol: String, _ label: String, enabled: Bool, _ action: @escaping () -> Void) -> some View {
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
}

/// One button per session the words are about (#261): a click brings up its terminal tab, as
/// its row in the session list does. They wrap onto more lines rather than squeeze.
struct SessionButtons: View {
    let sessions: [RosterRow]
    let onSession: (String) -> Void

    var body: some View {
        FlowLayout(spacing: 6) {
            ForEach(sessions) { r in
                Button { onSession(r.name) } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "terminal")
                        Text(r.label)
                            .lineLimit(1)
                            .truncationMode(.tail)
                        Image(systemName: "arrow.up.right")
                            .font(.caption2.weight(.bold))
                    }
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .frame(maxWidth: QuickChatCard.width - 16, alignment: .leading)
                    .background(Capsule().fill(.regularMaterial))
                    .overlay(Capsule().strokeBorder(.separator))
                    .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .help("Go to \(r.label)")
                .accessibilityLabel("Go to \(r.label)")
            }
        }
    }
}
