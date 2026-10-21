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
    let onSend: () -> Void
    let onStop: () -> Void
    let onClose: () -> Void

    static let width: CGFloat = 260
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if !model.shown.isEmpty || model.phase != .ready {
                Text("Pixie")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.top, 4)
                if !model.shown.isEmpty {
                    ScrollView {
                        Text(model.shown)
                            .font(.callout)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                    .frame(maxHeight: 180)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 8)
                }
                if model.phase == .stopped {
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
                if showsInput { Divider().padding(.top, 2) }
            }
            if showsInput {
                HStack(spacing: 6) {
                    TextField("Message Pixie", text: $state.draft)
                        .textFieldStyle(.plain)
                        .font(.callout)
                        .focused($focused)
                        .onSubmit(onSend)
                        .onExitCommand(perform: onClose)
                    Button(action: onSend) {
                        Image(systemName: "arrow.up.circle.fill")
                            .font(.title3)
                            .foregroundStyle(model.canSend(state.draft) ? .primary : .tertiary)
                    }
                    .buttonStyle(.plain)
                    .disabled(!model.canSend(state.draft))
                    .help("Send")
                }
                .padding(.horizontal, 8)
                .padding(.vertical, 4)
            }
        }
        .padding(6)
        .frame(width: Self.width, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
        .onAppear { if showsInput { focused = true } }
        .onChange(of: state.focusRequests) { focused = true }
    }
}
