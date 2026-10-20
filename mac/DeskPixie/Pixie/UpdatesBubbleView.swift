import AppKit
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
    /// Allow or Deny on a permission question (token, allow). The controller ignores a tap that
    /// comes too soon after the buttons appear (`UpdatesBubbleModel.acceptsDecision`).
    let onDecide: (String, Bool) -> Void
    /// A session menu's option (token, option from 1), under the same stray-click guard (#105).
    let onChoose: (String, Int) -> Void
    /// A click on the badge (opens the bubble, like hovering).
    let onBadge: () -> Void

    /// The bubble sizes to its lines: this narrow for short updates, as wide as a long question
    /// or option needs up to maxWidth, wrapping beyond it (#105). Nothing a question shows is cut.
    static let width: CGFloat = 240
    static let maxWidth: CGFloat = 440

    /// The width the shown lines need: their longest text on one line (a question's text, its
    /// options and note), plus the row's padding, between width and maxWidth.
    static func width(for lines: [UpdateLine]) -> CGFloat {
        let callout = NSFont.preferredFont(forTextStyle: .callout)
        let small = NSFont.systemFont(ofSize: NSFont.smallSystemFontSize)
        let mono = NSFont.monospacedSystemFont(ofSize: NSFont.preferredFont(forTextStyle: .caption1).pointSize, weight: .regular)
        func one(_ s: String, _ f: NSFont) -> CGFloat { (s as NSString).size(withAttributes: [.font: f]).width }
        var need: CGFloat = 0
        for l in lines where l.kind == "question" {
            need = max(need, one(l.shownText, callout) + 48)                       // icon, row and bubble padding
            for o in l.options ?? [] { need = max(need, one(o, small) + 52) } // button and bubble padding
            for n in (l.note ?? "").split(separator: "\n") { need = max(need, one(String(n), mono) + 48) }
        }
        return min(max(ceil(need), width), maxWidth)
    }

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
                VStack(alignment: .leading, spacing: 0) {
                    Button { onLine(line) } label: { row(line) }
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
                                    Text(option)
                                        .multilineTextAlignment(.leading)
                                        .fixedSize(horizontal: false, vertical: true)
                                        .frame(maxWidth: .infinity, alignment: .leading)
                                }
                                .buttonStyle(OptionButtonStyle(prominent: i == 0))
                            }
                        }
                        .controlSize(.small)
                        .padding(.horizontal, 8)
                        .padding(.bottom, 4)
                    }
                }
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
        .frame(width: Self.width(for: shown), alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
        .fixedSize(horizontal: false, vertical: true)
    }

    private func row(_ line: UpdateLine) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: 6) {
            if line.kind == "question" || line.asking == true {
                Image(systemName: "questionmark.circle")
                    .foregroundStyle(.orange)
            }
            VStack(alignment: .leading, spacing: 1) {
                if let title = line.title, !title.isEmpty {
                    Text(title)
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                        .truncationMode(.tail)
                }
                Text(line.shownText)
                    .font(.callout)
                    .lineLimit(line.kind == "question" ? nil : 2) // a question is read in full
                    .truncationMode(.tail)
                    .fixedSize(horizontal: false, vertical: line.kind == "question")
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
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 4)
        .contentShape(Rectangle())
    }
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
