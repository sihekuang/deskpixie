import DeskPixieCore
import SwiftUI

/// The message box pinned under the thread (spec §4.3): the "Replying to docs ×" chip, the
/// attachments waiting, + to attach, 1–5 lines (Return sends, Shift-Return adds a line), and
/// Send, which turns into Stop while a typed message is at work.
struct ChatComposer: View {
    let model: ChatThreadModel
    @Bindable var state: ChatWindowState
    /// The attachments waiting for the next request, shared with the box under Pixie; nil
    /// before Pixie is up. Read on each draw (each open bumps `focusRequests`), so Pixie
    /// coming up after the window opened is picked up.
    let attachmentsNow: () -> AttachmentsController?

    @FocusState private var focused: Bool

    private var attachments: AttachmentsController? { attachmentsNow() }

    var body: some View {
        let attachments = attachments
        return VStack(alignment: .leading, spacing: 8) {
            if let who = model.replyTo {
                replyChip(who)
            }
            if let attachments, attachments.model.chipsVisible {
                AttachmentChips(items: attachments.model.items, onRemove: { attachments.remove($0) })
            }
            if let notice = attachments?.model.notice ?? model.error {
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            HStack(alignment: .bottom, spacing: 8) {
                Button { attachments?.pick() } label: {
                    Image(systemName: "plus")
                        .font(.system(size: 15, weight: .medium))
                        .foregroundStyle(.primary)
                        .frame(width: 32, height: 32)
                        .background(RoundedRectangle(cornerRadius: 8).fill(ChatColors.field))
                        .contentShape(RoundedRectangle(cornerRadius: 8))
                }
                .buttonStyle(.plain)
                .disabled(attachments == nil)
                .help("Attach files")
                .accessibilityLabel("Attach files")
                TextField(model.placeholder, text: $state.draft, axis: .vertical)
                    .textFieldStyle(.plain)
                    .font(.system(size: 14))
                    .lineLimit(1...5)
                    .padding(.horizontal, 4)
                    .padding(.vertical, 7)
                    .focused($focused)
                    .accessibilityLabel("Message")
                    .accessibilityIdentifier("chat.composer")
                    // Return sends (after an input method has taken its own Return);
                    // Shift-Return adds a line where the cursor is.
                    .onSubmit(send)
                    .onKeyPress(.return, phases: .down) { press in
                        guard press.modifiers.contains(.shift) else { return .ignored }
                        NSApp.sendAction(#selector(NSResponder.insertNewlineIgnoringFieldEditor(_:)), to: nil, from: nil)
                        return .handled
                    }
                    // With text in the field, Esc is the field editor's "complete": take it first.
                    .onKeyPress(.escape) {
                        guard model.replyTo != nil else { return .ignored }
                        model.cancelReply()
                        return .handled
                    }
                if model.thinking && model.replyTo == nil {
                    Button { Task { await model.stop() } } label: {
                        RoundedRectangle(cornerRadius: 2)
                            .fill(Color(nsColor: .textBackgroundColor))
                            .frame(width: 10, height: 10)
                            .frame(width: 32, height: 32)
                            .background(Circle().fill(Color.primary))
                            .contentShape(Circle())
                    }
                    .buttonStyle(.plain)
                    .help("Stop")
                    .accessibilityLabel("Stop")
                } else {
                    Button(action: send) {
                        Image(systemName: "arrow.up")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(.white)
                            .frame(width: 32, height: 32)
                            .background(Circle().fill(canSend ? ChatColors.bubble : ChatColors.border))
                            .contentShape(Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(!canSend)
                    .help("Send")
                    .accessibilityLabel("Send")
                }
            }
            .padding(EdgeInsets(top: 8, leading: 10, bottom: 8, trailing: 8))
            .background(RoundedRectangle(cornerRadius: 14).fill(Color(nsColor: .textBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(ChatColors.border))
            .contentShape(Rectangle())
            .onTapGesture { focused = true }
            Text("Return sends · Shift-Return adds a line · drop files to attach")
                .font(.system(size: 11))
                .foregroundStyle(ChatColors.faint)
                .frame(maxWidth: .infinity)
        }
        .onAppear { focused = true }
        .onChange(of: state.focusRequests) { focused = true }
    }

    /// "Replying to docs ×": × (or Esc) goes back to messaging Pixie.
    private func replyChip(_ who: ChatWho) -> some View {
        let name = ChatThreadModel.displayName(who)
        return HStack(spacing: 6) {
            SessionDot(color: who.color, shown: who.kind == "session")
            Text("Replying to \(name)")
            Button { model.cancelReply() } label: {
                Image(systemName: "xmark")
                    .font(.system(size: 10, weight: .bold))
                    .frame(width: 24, height: 24)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help("Stop replying to \(name)")
            .accessibilityLabel("Stop replying to \(name)")
        }
        .font(.system(size: 12))
        .foregroundStyle(ChatColors.session(who.color))
        .padding(.leading, 10)
        .padding(.trailing, 4)
        .padding(.vertical, 1)
        .background(Capsule().fill(ChatColors.session(who.color).opacity(0.12)))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.replyChip")
    }

    private var paths: [String] { attachments?.model.items.map(\.path) ?? [] }

    private var canSend: Bool {
        !state.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !paths.isEmpty
    }

    /// The text and the attachments' paths go through `chat.send` (or the typed answer); a
    /// failed send puts the words back and keeps the copy on offer.
    private func send() {
        let text = state.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let paths = paths
        let attachments = attachments
        guard canSend else { return }
        state.draft = ""
        Task {
            await model.send(text, attachments: paths)
            if model.error == nil {
                attachments?.sent()
            } else if state.draft.isEmpty {
                state.draft = text
            }
        }
    }
}
