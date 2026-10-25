import DeskPixieCore
import SwiftUI

/// What the window keeps across redraws: the draft, requests to scroll or focus the box, and
/// whether the thread may count as seen.
@MainActor
@Observable
final class ChatWindowState {
    var draft = ""
    /// The draft put back after a send the core refused: its change isn't an edit, so the
    /// refusal stays under the box.
    var keptDraft: String?
    /// The @ list: where the @ whose list Esc closed is, and the row picked by ↑/↓ (nil: none
    /// yet, the first row shows as picked).
    var mentionClosedAt: Int?
    var mentionRow: Int?
    /// Bumped to put the cursor in the box.
    var focusRequests = 0
    /// The entry to scroll to, bumped by `scrollRequests` (opening at a message, a pill).
    private(set) var scrollTarget: String?
    private(set) var scrollRequests = 0
    /// The opening scroll has run since the window last opened: until then, a page arriving
    /// neither scrolls nor counts as read, and the top doesn't load older pages.
    var opened: Bool { open.opened }
    /// A view that appears runs this open's scroll if it was asked for before the view saw it.
    var scrollsOnAppear: Bool { open.scrollsOnAppear }
    private var open = ChatOpening()
    /// The window is on screen: not hidden behind others, minimized or on another Space (the
    /// controller sets it from the window's occlusion state).
    var visible = false

    func scroll(to id: String?) {
        scrollTarget = id
        scrollRequests += 1
        open.asked()
    }

    /// The window is opening: wait for its own first scroll again.
    func opening() { open.opening() }

    /// A scroll ran: the window is open.
    func scrolled() { open.scrolled() }
}

/// The chat window (#302, spec §4): the needs strip, the thread in a column at most 680 pt
/// wide, and the message box.
struct ChatWindowView: View {
    let model: ChatThreadModel
    let state: ChatWindowState
    /// Read on each draw: Pixie (and its attachments) may come up after the window opened.
    let attachments: () -> AttachmentUI?
    let actions: ChatActions

    static let column: CGFloat = 680

    var body: some View {
        VStack(spacing: 0) {
            if !model.needs.isEmpty {
                NeedsStrip(needs: model.needs) { state.scroll(to: $0) }
            }
            ChatThreadView(model: model, state: state, actions: actions)
            Rectangle().fill(ChatColors.rule).frame(height: 1)
            ChatComposer(model: model, state: state, attachmentsNow: attachments)
                .frame(maxWidth: Self.column)
                .padding(EdgeInsets(top: 12, leading: 20, bottom: 16, trailing: 20))
                .frame(maxWidth: .infinity)
        }
        .frame(minWidth: 400, minHeight: 480)
        .background(Color(nsColor: .textBackgroundColor))
        .onDrop(of: [.fileURL, .image], isTargeted: nil) { attachments()?.onDrop($0) ?? false }
    }
}

/// "2 need you", then a pill per request; a pill scrolls to its card.
private struct NeedsStrip: View {
    let needs: [ChatEntry]
    let onPick: (String) -> Void

    var body: some View {
        FlowLayout(spacing: 10) {
            Text(ChatThreadModel.needsLabel(needs.count))
                .font(.system(size: 14, weight: .semibold))
                .foregroundStyle(ChatColors.needsTitle)
                .padding(.vertical, 4)
            ForEach(needs) { e in
                Button { onPick(e.id) } label: {
                    HStack(spacing: 6) {
                        SessionDot(color: e.who.color, shown: e.who.kind == "session")
                        Text(ChatThreadModel.pillLabel(e))
                            .lineLimit(1)
                            .truncationMode(.tail)
                            .frame(maxWidth: 320, alignment: .leading)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    .font(.system(size: 13))
                    .foregroundStyle(ChatColors.needsPillText)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 4)
                    .background(Capsule().fill(ChatColors.pill))
                    .overlay(Capsule().strokeBorder(ChatColors.needsEdge))
                    .contentShape(Capsule())
                }
                .buttonStyle(.plain)
                .help("Show it in the chat")
                .accessibilityIdentifier("chat.needs.pill.\(e.id)")
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(ChatColors.needsStrip)
        .overlay(alignment: .bottom) { Rectangle().fill(ChatColors.needsRule).frame(height: 1) }
    }
}

/// The thread's content in the scroll view's coordinates: where it ends (after its bottom
/// padding) and how tall it is. Measured outside the lazy stack, so it is always laid out. Set
/// straight from the content's own GeometryReader: a preference set inside a macOS ScrollView's
/// content never reaches `onPreferenceChange` outside it.
private struct ThreadGeometry: Equatable {
    var bottom = CGFloat.infinity
    var height: CGFloat = 0
}

/// The thread, newest at the bottom. It opens at the message asked for, else the oldest unread,
/// else the newest. Scrolled up, something new shows ↓ Latest instead of the view jumping. The
/// newest entry counts as read (`sawBottom`) only when the end is in view, measured from the
/// layout, with the window on screen and after the opening scroll.
private struct ChatThreadView: View {
    let model: ChatThreadModel
    let state: ChatWindowState
    let actions: ChatActions

    private static let bottomID = "chat.bottom"
    private static let space = "chat.scroll"
    @State private var geometry = ThreadGeometry()
    @State private var viewport: CGFloat = 0
    /// A scroll was asked for and its layout hasn't been reported yet: nothing counts as read.
    @State private var awaitingLayout = false
    /// Which scroll `awaitingLayout` waits on, so a late give-up can't end a newer wait.
    @State private var layoutWaits = 0

    /// Held at the bottom through the content's height flickering (`ChatBottom`).
    @State private var bottom = ChatBottom()

    /// From the layout as it stands, so it holds from the first layout, reported or not.
    private var atBottom: Bool {
        bottom.atBottom(sentinelMaxY: Double(geometry.bottom), viewportHeight: Double(viewport))
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 0) {
                LazyVStack(alignment: .leading, spacing: 18) {
                    if model.hasOlder {
                        ProgressView()
                            .controlSize(.small)
                            .frame(maxWidth: .infinity)
                            .onAppear { if state.opened { Task { await model.loadOlder() } } }
                    }
                    ForEach(Array(model.entries.enumerated()), id: \.element.id) { i, e in
                        row(e, after: i > 0 ? model.entries[i - 1] : nil)
                    }
                    // Words still coming, after every entry: yours, then Pixie's.
                    ForEach(["you", "pixie"], id: \.self) { who in
                        if let text = model.liveText(who) {
                            ChatLiveView(line: ChatLive(who: who, text: text))
                        }
                    }
                    if model.showsThinking {
                        HStack(spacing: 8) {
                            Circle().fill(Color.secondary).frame(width: 6, height: 6)
                            Text(model.thinkingLabel)
                                .lineLimit(2)
                                .truncationMode(.tail)
                        }
                        .font(.system(size: 13))
                        .foregroundStyle(ChatColors.label)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier(model.coordinatorStep == nil ? "chat.thinking" : "chat.live.coordinator")
                    }
                }
                .frame(maxWidth: ChatWindowView.column)
                .padding(.horizontal, 24)
                .padding(.vertical, 20)
                .frame(maxWidth: .infinity)
                // The content's very end, after the padding: the opening scroll and ↓ Latest
                // go here, so a scroll to the bottom shows the padding too and ends with the
                // end at the viewport's edge. (Under the last row, a scroll lined up the row and
                // left the padding below on macOS 14/15, past at-bottom's tolerance: #316.)
                Color.clear
                    .frame(height: 1)
                    .id(Self.bottomID)
                }
                .background(GeometryReader { g in
                    let f = g.frame(in: .named(Self.space))
                    let now = ThreadGeometry(bottom: f.maxY, height: f.height)
                    Color.clear
                        .onAppear { geometry = now }
                        .onChange(of: now) { _, n in geometry = n }
                })
            }
            .coordinateSpace(name: Self.space)
            .background(GeometryReader { g in
                Color.clear
                    .onAppear { viewport = g.size.height }
                    .onChange(of: g.size.height) { _, new in viewport = new }
            })
            .onChange(of: viewport) { old, new in
                // The box under the thread grew (a chip, the @ list, more lines): at the bottom
                // before, stay there instead of showing ↓ Latest.
                if bottom.resized(sentinelMaxY: Double(geometry.bottom), oldViewport: Double(old),
                                  newViewport: Double(new), following: state.opened && !awaitingLayout) {
                    proxy.scrollTo(Self.bottomID, anchor: .bottom)
                }
            }
            .onChange(of: geometry) { old, new in
                // The layout after the opening scroll: only now does at-bottom hold, from where
                // the end is.
                if awaitingLayout {
                    awaitingLayout = false
                    bottom.settled()
                    markRead()
                    return
                }
                // Rows grow after they are drawn (links found, text wrapped), and live lines grow
                // as the words come: at the bottom, stay there.
                if bottom.laidOut(oldMaxY: Double(old.bottom), oldHeight: Double(old.height),
                                  newMaxY: Double(new.bottom), newHeight: Double(new.height),
                                  viewportHeight: Double(viewport), following: state.opened) {
                    proxy.scrollTo(Self.bottomID, anchor: .bottom)
                }
            }
            .accessibilityIdentifier("chat.thread")
            .overlay(alignment: .bottom) {
                if state.opened && !atBottom {
                    Button {
                        withAnimation { proxy.scrollTo(Self.bottomID, anchor: .bottom) }
                    } label: {
                        Text(model.unread > 0 ? "↓ Latest (\(model.unread))" : "↓ Latest")
                            .font(.system(size: 12, weight: .semibold))
                            .padding(.horizontal, 12)
                            .padding(.vertical, 6)
                            .background(Capsule().fill(.regularMaterial))
                            .overlay(Capsule().strokeBorder(ChatColors.border))
                            .contentShape(Capsule())
                    }
                    .buttonStyle(.plain)
                    .padding(.bottom, 12)
                    .accessibilityIdentifier("chat.latest")
                }
            }
            .onChange(of: model.entries.last?.id) {
                // A page swapped in by the opening load waits for the opening scroll.
                guard state.opened, atBottom else { return }
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
                markRead()
            }
            .onChange(of: model.showsThinking) {
                if state.opened && atBottom { proxy.scrollTo(Self.bottomID, anchor: .bottom) }
            }
            .onChange(of: atBottom) { markRead() }
            .onChange(of: state.visible) { markRead() }
            .onChange(of: state.scrollRequests) { scroll(proxy, to: state.scrollTarget) }
            // A request made before this view's first update never reaches onChange.
            // Only this open's request: a view made for a new open must not run the last one's.
            .onAppear { if state.scrollsOnAppear { scroll(proxy, to: state.scrollTarget) } }
        }
    }

    private func markRead() {
        guard !awaitingLayout else { return }
        if ChatThreadModel.marksRead(opened: state.opened, visible: state.visible, atBottom: atBottom) {
            model.sawBottom()
        }
    }

    private func row(_ e: ChatEntry, after previous: ChatEntry?) -> some View {
        VStack(spacing: 18) {
            if ChatThreadModel.startsNewTime(after: previous, e) {
                Text(ChatTime.divider(e.at))
                    .font(.system(size: 12))
                    .foregroundStyle(ChatColors.faint)
                    .frame(maxWidth: .infinity)
            }
            ChatEntryView(entry: e, actions: actions, who: { model.who(forSession: $0) })
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("chat.entry.\(e.id)")
        }
        .id(e.id)
    }

    /// A window shown again after a hide keeps its rows, so its opening scroll may not move
    /// anything (already at the bottom, or the end can't scroll further) and no layout report
    /// follows: when none has come in a moment, what is known holds.
    private func giveUpWaiting() {
        layoutWaits += 1
        let mine = layoutWaits
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3) {
            guard awaitingLayout, layoutWaits == mine else { return }
            awaitingLayout = false
            markRead()
        }
    }

    /// To an entry (nil: where the thread opens). The first one, once laid out, opens the
    /// thread: from then on new entries scroll and count as read.
    private func scroll(_ proxy: ScrollViewProxy, to id: String?) {
        let target = id ?? model.openTarget
        DispatchQueue.main.async {
            if let target, target != model.entries.last?.id, model.entries.contains(where: { $0.id == target }) {
                // The oldest unread (or the message asked for) at the top, what came after below.
                proxy.scrollTo(target, anchor: .top)
            } else {
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
            // The geometry known now is from before the scroll: the next report decides. Content
            // that fits doesn't move, so what is known holds.
            if !state.opened {
                awaitingLayout = ChatThreadModel.scrollMoves(contentHeight: Double(geometry.height),
                                                             viewportHeight: Double(viewport))
                if awaitingLayout { giveUpWaiting() }
            }
            state.scrolled()
            markRead()
        }
    }
}
