import DeskPixieCore
import SwiftUI

/// What the window keeps across redraws: the draft, requests to scroll or focus the box, and
/// whether the thread may count as seen.
@MainActor
@Observable
final class ChatWindowState {
    var draft = ""
    /// Bumped to put the cursor in the box.
    var focusRequests = 0
    /// The entry to scroll to, bumped by `scrollRequests` (opening at a message, a pill).
    private(set) var scrollTarget: String?
    private(set) var scrollRequests = 0
    /// The opening scroll has run since the window last opened: until then, a page arriving
    /// neither scrolls nor counts as read, and the top doesn't load older pages.
    private(set) var opened = false
    /// The window is on screen: not hidden behind others, minimized or on another Space (the
    /// controller sets it from the window's occlusion state).
    var visible = false

    func scroll(to id: String?) {
        scrollTarget = id
        scrollRequests += 1
    }

    /// The window is opening: wait for its first scroll again.
    func opening() { opened = false }

    func didOpen() { opened = true }
}

/// The chat window (#302, spec §4): the needs strip, the thread in a column at most 680 pt
/// wide, and the message box.
struct ChatWindowView: View {
    let model: ChatThreadModel
    let state: ChatWindowState
    /// Read on each draw: Pixie (and its attachments) may come up after the window opened.
    let attachments: () -> AttachmentsController?
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
        .onDrop(of: [.fileURL, .image], isTargeted: nil) { attachments()?.drop($0) ?? false }
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

/// Where the bottom marker's lower edge is, in the scroll view's coordinates (infinite while
/// it isn't laid out: a lazy stack drops it far off screen).
private struct BottomEdgeKey: PreferenceKey {
    static let defaultValue = CGFloat.infinity
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = nextValue() }
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
    @State private var bottomEdge = CGFloat.infinity
    @State private var viewport: CGFloat = 0

    private var atBottom: Bool {
        ChatThreadModel.isAtBottom(sentinelMaxY: Double(bottomEdge), viewportHeight: Double(viewport))
    }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
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
                    if model.thinking {
                        HStack(spacing: 8) {
                            Circle().fill(Color.secondary).frame(width: 6, height: 6)
                            Text("Thinking…")
                        }
                        .font(.system(size: 13))
                        .foregroundStyle(ChatColors.label)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.bottomID)
                        .background(GeometryReader { g in
                            Color.clear.preference(key: BottomEdgeKey.self, value: g.frame(in: .named(Self.space)).maxY)
                        })
                }
                .frame(maxWidth: ChatWindowView.column)
                .padding(.horizontal, 24)
                .padding(.vertical, 20)
                .frame(maxWidth: .infinity)
            }
            .defaultScrollAnchor(.bottom)
            .coordinateSpace(name: Self.space)
            .background(GeometryReader { g in
                Color.clear
                    .onAppear { viewport = g.size.height }
                    .onChange(of: g.size.height) { viewport = g.size.height }
            })
            .onPreferenceChange(BottomEdgeKey.self) { bottomEdge = $0 }
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
            .onChange(of: model.thinking) {
                if state.opened && atBottom { proxy.scrollTo(Self.bottomID, anchor: .bottom) }
            }
            .onChange(of: atBottom) { markRead() }
            .onChange(of: state.visible) { markRead() }
            .onChange(of: state.opened) { markRead() }
            .onChange(of: state.scrollRequests) { scroll(proxy, to: state.scrollTarget) }
        }
    }

    private func markRead() {
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

    /// To an entry (nil: where the thread opens). The first one, once laid out, opens the
    /// thread: from then on new entries scroll and count as read.
    private func scroll(_ proxy: ScrollViewProxy, to id: String?) {
        let target = id ?? model.openTarget
        DispatchQueue.main.async {
            if let target, target != model.entries.last?.id, model.entries.contains(where: { $0.id == target }) {
                proxy.scrollTo(target, anchor: .center)
            } else {
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
            if !state.opened { state.didOpen() }
        }
    }
}
