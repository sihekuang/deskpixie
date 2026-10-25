import AppKit
import DeskPixieCore
import SwiftUI

/// What the text box (or the card under Pixie) needs of the attachments (#240).
struct AttachmentUI {
    let model: AttachmentModel
    /// "Screenshot copied" while something copied isn't attached yet (text box only).
    let hint: String?
    let onAttachHint: () -> Void
    let onRemove: (String) -> Void
    let onPick: () -> Void
    let onDrop: ([NSItemProvider]) -> Bool
    /// Cmd-V: attaches when the clipboard holds only an image or files (true), else the text
    /// pastes (the chat window's box, #302).
    var onPaste: () -> Bool = { false }
    /// The message went: the hint is for a newer copy only.
    var onSent: () -> Void = {}

    @MainActor static let none = AttachmentUI(model: AttachmentModel(), hint: nil, onAttachHint: {}, onRemove: { _ in },
                                   onPick: {}, onDrop: { _ in false })
}

/// The named chips: a thumbnail or the file's icon, its name, and ✕. They wrap onto more lines.
struct AttachmentChips: View {
    let items: [AttachmentItem]
    let onRemove: (String) -> Void

    var body: some View {
        FlowLayout(spacing: 4) {
            ForEach(items) { item in
                HStack(spacing: 4) {
                    Image(nsImage: Self.thumbnail(item))
                        .resizable()
                        .aspectRatio(contentMode: .fill)
                        .frame(width: 22, height: 16)
                        .clipShape(RoundedRectangle(cornerRadius: 3))
                    Text(item.name)
                        .font(.caption)
                        .lineLimit(1)
                        .truncationMode(.middle)
                        .frame(maxWidth: 140, alignment: .leading)
                    Button { onRemove(item.id) } label: {
                        Image(systemName: "xmark")
                            .font(.caption2.weight(.bold))
                            .frame(width: 14, height: 14)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .help("Remove")
                    .accessibilityLabel("Remove \(item.name)")
                }
                .padding(.horizontal, 6)
                .padding(.vertical, 3)
                .background(RoundedRectangle(cornerRadius: 6).fill(.quaternary))
            }
        }
    }

    static func thumbnail(_ item: AttachmentItem) -> NSImage {
        if item.kind == "image", let image = NSImage(contentsOfFile: item.path) { return image }
        return NSWorkspace.shared.icon(forFile: item.path)
    }
}

/// Lays its views out in rows, wrapping at the proposed width.
struct FlowLayout: SwiftUI.Layout {
    var spacing: CGFloat = 4

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let rows = rows(width: proposal.width ?? .infinity, subviews)
        let height = rows.map(\.height).reduce(0, +) + spacing * CGFloat(max(0, rows.count - 1))
        return CGSize(width: proposal.width ?? rows.map(\.width).max() ?? 0, height: height)
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var y = bounds.minY
        for row in rows(width: bounds.width, subviews) {
            var x = bounds.minX
            for i in row.indices {
                let size = subviews[i].sizeThatFits(.unspecified)
                subviews[i].place(at: CGPoint(x: x, y: y), proposal: ProposedViewSize(size))
                x += size.width + spacing
            }
            y += row.height + spacing
        }
    }

    private struct Row { var indices: [Int] = []; var width: CGFloat = 0; var height: CGFloat = 0 }

    private func rows(width: CGFloat, _ subviews: Subviews) -> [Row] {
        var rows = [Row()]
        for (i, v) in subviews.enumerated() {
            let size = v.sizeThatFits(.unspecified)
            if !rows[rows.count - 1].indices.isEmpty && rows[rows.count - 1].width + spacing + size.width > width {
                rows.append(Row())
            }
            var r = rows[rows.count - 1]
            r.width += (r.indices.isEmpty ? 0 : spacing) + size.width
            r.height = max(r.height, size.height)
            r.indices.append(i)
            rows[rows.count - 1] = r
        }
        return rows.filter { !$0.indices.isEmpty }
    }
}
