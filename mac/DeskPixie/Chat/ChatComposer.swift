import DeskPixieCore
import SwiftUI

/// The message box pinned under the thread (spec §4.3): the "Replying to docs ×" chip, the
/// attachments waiting and the "Screenshot copied · Attach" row, + to attach, 1–5 lines
/// (Return sends, Shift-Return adds a line), and Send, which turns into Stop while a typed
/// message is at work.
struct ChatComposer: View {
    let model: ChatThreadModel
    @Bindable var state: ChatWindowState
    /// The attachments, as the box under Pixie has them (shared with it); nil before Pixie is
    /// up. Read on each draw (each open bumps `focusRequests`), so Pixie coming up after the
    /// window opened is picked up.
    let attachmentsNow: () -> AttachmentUI?

    @FocusState private var focused: Bool

    var body: some View {
        let attachments = attachmentsNow()
        let mention = self.mention
        return VStack(alignment: .leading, spacing: 8) {
            if let mention {
                MentionList(query: mention, candidates: mention.candidates(model.recipients),
                            selected: state.mentionRow) { pick($0, mention) }
            }
            if let who = model.replyTo {
                replyChip(who)
            }
            if let attachments, attachments.model.chipsVisible {
                AttachmentChips(items: attachments.model.items, onRemove: attachments.onRemove)
            }
            if let notice = model.notice(for: state.draft) {
                // One recipient per message: why Send is off, or why the core refused it.
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(ChatColors.needsTitle)
                    .accessibilityIdentifier("chat.notice")
            } else if let notice = attachments?.model.notice ?? model.error {
                Text(notice)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else if let attachments, let hint = attachments.hint {
                // Something copied and not attached yet (#240): one click attaches it.
                HStack(spacing: 4) {
                    Image(systemName: "doc.on.clipboard")
                    Text(hint)
                    Text("·")
                    Button("Attach", action: attachments.onAttachHint)
                        .buttonStyle(.link)
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
            HStack(alignment: .bottom, spacing: 8) {
                Button { attachments?.onPick() } label: {
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
                    .onSubmit { send(attachments) }
                    .onKeyPress(.return, phases: .down) { press in
                        if press.modifiers.contains(.shift) {
                            NSApp.sendAction(#selector(NSResponder.insertNewlineIgnoringFieldEditor(_:)), to: nil, from: nil)
                            return .handled
                        }
                        // With the @ list open, Return picks.
                        return pickSelected() ? .handled : .ignored
                    }
                    .onKeyPress(.tab) { pickSelected() ? .handled : .ignored }
                    .onKeyPress(.upArrow) { moveMention(-1) }
                    .onKeyPress(.downArrow) { moveMention(1) }
                    // With text in the field, Esc is the field editor's "complete": take it first.
                    // It closes the @ list (the text stays), else stops the reply.
                    .onKeyPress(.escape) {
                        if let m = mention {
                            state.mentionClosedAt = m.start
                            return .handled
                        }
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
                    let can = canSend(attachments)
                    Button { send(attachments) } label: {
                        Image(systemName: "arrow.up")
                            .font(.system(size: 14, weight: .bold))
                            .foregroundStyle(.white)
                            .frame(width: 32, height: 32)
                            .background(Circle().fill(can ? ChatColors.bubble : ChatColors.border))
                            .contentShape(Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(!can)
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
        .onChange(of: state.draft) { _, new in
            state.mentionRow = 0
            if let closed = state.mentionClosedAt, MentionQuery.active(in: new, caret: new.count)?.start != closed {
                state.mentionClosedAt = nil // another @: its list opens
            }
            if new == state.keptDraft {
                state.keptDraft = nil
                return
            }
            model.draftChanged()
        }
    }

    /// The @ being typed, while its list isn't closed. The caret is taken to be at the end of the
    /// text: SwiftUI's field doesn't say where it is on macOS 14.
    private var mention: MentionQuery? {
        guard let q = MentionQuery.active(in: state.draft, caret: state.draft.count),
              q.start != state.mentionClosedAt else { return nil }
        return q
    }

    /// ↑/↓ in the @ list.
    private func moveMention(_ by: Int) -> KeyPress.Result {
        guard let m = mention else { return .ignored }
        let n = m.candidates(model.recipients).count
        guard n > 0 else { return .handled }
        state.mentionRow = (state.mentionRow + by + n) % n
        return .handled
    }

    /// Return or Tab with the list open: the row picked. False with no list, or nothing in it
    /// ("No one called …"): Return sends.
    private func pickSelected() -> Bool {
        guard let m = mention, let row = m.selected(row: state.mentionRow, model.recipients) else { return false }
        pick(row, m)
        return true
    }

    /// The pick is the one target (Pixie: none), and the @… leaves the text.
    private func pick(_ c: MentionCandidate, _ m: MentionQuery) {
        let (text, target) = m.pick(c, in: state.draft, caret: state.draft.count, previous: model.replyTo.map { ChatRecipient(name: $0.name, label: $0.label) })
        state.draft = text
        model.choose(target)
        focused = true
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

    private func canSend(_ attachments: AttachmentUI?) -> Bool {
        model.canSend(state.draft, attachments: attachments?.model.items.count ?? 0)
    }

    /// The text and the attachments' paths go through `chat.send` (or the typed answer); a
    /// failed send puts the words back and keeps the copy on offer. One at a time: the model
    /// refuses a second while the first is on its way.
    private func send(_ attachments: AttachmentUI?) {
        guard canSend(attachments) else { return }
        let text = state.draft.trimmingCharacters(in: .whitespacesAndNewlines)
        let paths = attachments?.model.items.map(\.path) ?? []
        state.draft = ""
        Task {
            guard await model.send(text, attachments: paths) else {
                if state.draft.isEmpty {
                    state.keptDraft = text
                    state.draft = text
                }
                return
            }
            attachments?.onSent()
        }
    }
}

/// The @ list above the box (#302): Pixie, then who a message can go to on its own, filtered by
/// what follows the @. ↑/↓ move, Return or Tab picks, Esc closes; a click picks.
private struct MentionList: View {
    let query: MentionQuery
    let candidates: [MentionCandidate]
    let selected: Int
    let onPick: (MentionCandidate) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if candidates.isEmpty {
                Text(query.emptyLabel())
                    .font(.system(size: 13))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 8)
            }
            let picked = min(selected, candidates.count - 1)
            ForEach(Array(candidates.enumerated()), id: \.element.id) { i, c in
                Button { onPick(c) } label: {
                    HStack(spacing: 8) {
                        if case .recipient(let r) = c {
                            SessionDot(color: r.color)
                        } else {
                            Image(systemName: "sparkles")
                                .font(.system(size: 10))
                                .frame(width: 8)
                                .accessibilityHidden(true)
                        }
                        Text(c.label)
                            .fontWeight(.semibold)
                            .lineLimit(1)
                        Text(c.detail)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                        Spacer(minLength: 0)
                    }
                    .font(.system(size: 13))
                    .padding(.horizontal, 12)
                    .padding(.vertical, 6)
                    .background(RoundedRectangle(cornerRadius: 6)
                        .fill(i == picked ? Color.accentColor.opacity(0.18) : .clear))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(c.label), \(c.detail)")
                .accessibilityAddTraits(i == picked ? .isSelected : [])
                .accessibilityIdentifier("chat.mention.row")
            }
        }
        .padding(4)
        .frame(maxWidth: 360, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10).fill(Color(nsColor: .textBackgroundColor)))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(ChatColors.border))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("chat.mention.list")
    }
}
