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

/// The round button after the toggle (#302): shows the chat window, or hides it while it's
/// shown (filled then), with a dot for unread while it's hidden. Drawn like the toggle.
struct ChatWindowButton: View {
    let toggle: ChatToggle
    let onClick: () -> Void

    /// The toggle's height: its 26 pt segments and 3 pt padding.
    static let size: CGFloat = 32

    var body: some View {
        Button(action: onClick) {
            Image(systemName: "list.bullet.rectangle")
                .font(.title3.weight(.semibold))
                .foregroundStyle(toggle.filled ? AnyShapeStyle(.white) : AnyShapeStyle(.primary))
                .frame(width: Self.size, height: Self.size)
                .background(Circle().fill(toggle.filled ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.regularMaterial)))
                .overlay(Circle().strokeBorder(toggle.filled ? AnyShapeStyle(Color.accentColor) : AnyShapeStyle(.separator)))
                .overlay(alignment: .topTrailing) {
                    if toggle.dot {
                        Circle()
                            .fill(Color.blue)
                            .frame(width: 10, height: 10)
                            .overlay(Circle().strokeBorder(.background, lineWidth: 2))
                            .offset(x: 2, y: -2)
                    }
                }
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .help(toggle.help)
        .accessibilityLabel(toggle.label)
        .accessibilityAddTraits(toggle.filled ? .isSelected : [])
        .accessibilityIdentifier("pixie.chatButton")
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
    let onResize: () -> Void
    let onDismiss: () -> Void
    var onStep: (ReplyStep) -> Void = { _ in }
    /// The card shows above Pixie (#286): the deck's shorter cards sit at its bottom, next to it.
    var above = false
    /// A scroll to another page of the deck (#286), by its id, and a swipe starting or ending.
    var onPage: (Int) -> Void = { _ in }
    var onScroll: (Bool) -> Void = { _ in }
    /// The chips, the hint row, 📎 and drops (#240).
    let attachments: AttachmentUI
    /// Buttons to the sessions a page's words are about (#261), and what a click does.
    var rows: ([String]) -> [RosterRow] = { _ in [] }
    var onSession: (String) -> Void = { _ in }

    static let width: CGFloat = 260
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if showsDeck {
                // The replies side by side (#286), each its own card: a two-finger swipe or a
                // card's arrows page them.
                DeckPager(ids: model.pages.map(\.id), selected: model.pageID, width: Self.width,
                          onSelect: onPage, onResize: onResize, onScroll: onScroll, bottomAligned: above) { id in
                    if let i = model.pages.firstIndex(where: { $0.id == id }) { replyCard(i) }
                }
            } else if model.showsEarlierOnly {
                HStack(spacing: 4) {
                    Text("Earlier replies")
                    Spacer(minLength: 4)
                    ReplyArrows(model: model, position: nil, onStep: onStep)
                    DeckHideButton(action: onDismiss)
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
                .padding(8)
                .deckCard()
            }
            if showsBox {
                VStack(alignment: .leading, spacing: 4) {
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
                .deckCard()
            }
        }
        .frame(width: Self.width, alignment: .leading)
        .onDrop(of: [.fileURL, .image], isTargeted: nil, perform: attachments.onDrop)
        .onAppear { if showsInput { focused = true } }
        .onChange(of: state.focusRequests) { focused = true }
        // The panel is sized from the view: a taller field needs a new frame.
        .onChange(of: state.draft) { onResize() }
        .onChange(of: attachments.model) { onResize() }
    }

    /// Words to page through: what is up now, or a kept reply you went back to.
    private var showsDeck: Bool { model.pageID != nil && !(model.shownText.isEmpty && model.pages.count < 2) }

    /// The box under the cards: work in progress, the attachments and the line to type in.
    private var showsBox: Bool {
        (model.phase == .stopped && model.viewed == nil) || model.statusText != nil || showsInput
            || attachments.model.chipsVisible || attachments.model.notice != nil
    }

    /// One reply as its own card (#286): its header with the time, ‹ n/m › and Hide, its words (typed out
    /// word by word; links and files are found in all of it, #277), scrolling when long, and
    /// buttons to the sessions it is about. Its arrows page the deck from this card.
    private func replyCard(_ i: Int) -> some View {
        let pages = model.pages
        let p = pages[i]
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                TimelineView(.periodic(from: .now, by: 30)) { context in
                    Text(p.header(now: context.date))
                        .lineLimit(1)
                }
                Spacer(minLength: 4)
                if pages.count > 1 {
                    DeckArrows(index: i, count: pages.count, label: "/",
                               onBack: { onPage(pages[i - 1].id) }, onForward: { onPage(pages[i + 1].id) })
                }
                if model.newerWaiting && i < pages.count - 1 {
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
                if model.canHide {
                    // Puts the deck away (#224, #286), as on the badge deck; the setting puts it
                    // away on its own after a while.
                    DeckHideButton(action: onDismiss)
                }
            }
            .font(.caption.weight(.semibold))
            .foregroundStyle(.secondary)
            if !p.shown.isEmpty {
                ScrollView {
                    RichMessage(text: p.text, shown: p.shown, sessions: p.sessions, selectable: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .frame(maxHeight: 180)
                .fixedSize(horizontal: false, vertical: true)
            }
            let buttons = rows(p.sessions)
            if !buttons.isEmpty {
                SessionButtons(sessions: buttons, onSession: onSession,
                               maxWidth: Self.width - 2 * DeckPager<Int, EmptyView>.peek - 16)
            }
        }
        .padding(8)
        .deckCard()
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
    var maxWidth: CGFloat = QuickChatCard.width - 16

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
                    .frame(maxWidth: maxWidth, alignment: .leading)
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
