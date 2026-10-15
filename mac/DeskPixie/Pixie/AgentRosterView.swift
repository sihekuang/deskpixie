import DeskPixieCore
import SwiftUI

/// The roster chip under Pixie and, when open, the list (spec docs/superpowers/specs/2026-10-15-agent-roster-design.md
/// §3). Drawn by `AgentRosterController` from its model; `below` puts the list under the chip.
struct AgentRosterView: View {
    let model: AgentRosterModel
    let below: Bool
    /// The chip is never wider than Pixie (it must not reach the updates bubble at Pixie's side).
    let maxChipWidth: CGFloat
    let onChip: () -> Void
    let onRow: (RosterRow) -> Void

    static let listWidth: CGFloat = 260

    var body: some View {
        VStack(spacing: 4) {
            if model.open && !below { list }
            chip
            if model.open && below { list }
        }
        .fixedSize()
    }

    private var chip: some View {
        Button(action: onChip) {
            Text(model.chipText)
                .font(.caption.weight(.semibold))
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.6)
                .padding(.horizontal, 7)
                .padding(.vertical, 3)
                .background(Capsule().fill(.regularMaterial))
                .overlay(Capsule().strokeBorder(.separator))
        }
        .buttonStyle(.plain)
        .frame(maxWidth: maxChipWidth)
        .accessibilityLabel("\(model.view.agentCount) of Pixie's agents, \(model.view.yourCount) of your sessions")
    }

    private var list: some View {
        VStack(alignment: .leading, spacing: 2) {
            ForEach(model.sections, id: \.title) { section in
                Text(section.title)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                    .padding(.horizontal, 8)
                    .padding(.top, 4)
                ForEach(section.rows) { r in
                    let clickable = AgentRosterModel.clickable(r)
                    Button { onRow(r) } label: { row(r) }
                        .buttonStyle(.plain)
                        .disabled(!clickable)
                        .onHover { inside in
                            // A pointing hand on clickable rows only (spec §4).
                            guard clickable else { return }
                            if inside { NSCursor.pointingHand.push() } else { NSCursor.pop() }
                        }
                }
                if section.more > 0 {
                    Text("+\(section.more) more")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                }
            }
        }
        .padding(6)
        .frame(width: Self.listWidth, alignment: .leading)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator))
    }

    private func row(_ r: RosterRow) -> some View {
        HStack(spacing: 6) {
            Text(AgentRosterModel.dot(r.status))
                .foregroundStyle(r.status == "working" ? .green : r.status == "blocked" ? .orange : .secondary)
            Text(r.label).font(.callout).lineLimit(1).truncationMode(.tail)
            Spacer(minLength: 4)
            Text(r.status).font(.caption).foregroundStyle(.secondary)
            if let d = r.detail, !d.isEmpty {
                Text(d).font(.caption).foregroundStyle(.tertiary).lineLimit(1)
            }
        }
        .opacity(r.reachable ? 1 : 0.45)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 8)
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }
}
