import DeskPixieCore
import SwiftUI

/// The updates bubble's content (spec §3): a count badge, or the bubble listing the latest
/// unheard lines newest first. Rows are buttons (a click focuses the line's herdr pane); the
/// "+N more" row starts a conversation. Drawn by `UpdatesBubbleController` from its model.
struct UpdatesBubbleView: View {
    let count: Int
    let phase: UpdatesBubbleModel.Phase
    let shown: [UpdateLine]
    let more: Int
    let onLine: (UpdateLine) -> Void
    let onMore: () -> Void
    /// A click on the badge (opens the bubble, like hovering).
    let onBadge: () -> Void

    static let width: CGFloat = 240

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

    private var bubble: some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(shown) { line in
                Button { onLine(line) } label: { row(line) }
                    .buttonStyle(.plain)
            }
            if more > 0 {
                Button(action: onMore) {
                    Text("+\(more) more")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
        .padding(6)
        .frame(width: Self.width, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
        .fixedSize(horizontal: false, vertical: true)
    }

    private func row(_ line: UpdateLine) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            if line.kind == "question" {
                Image(systemName: "questionmark.circle")
                    .foregroundStyle(.orange)
            }
            VStack(alignment: .leading, spacing: 1) {
                Text(line.text)
                    .font(.callout)
                    .lineLimit(2)
                    .truncationMode(.tail)
                (Text(Date(timeIntervalSince1970: Double(line.at) / 1000), style: .relative) + Text(" ago"))
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
