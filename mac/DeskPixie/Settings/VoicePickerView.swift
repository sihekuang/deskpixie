import DeskPixieCore
import SwiftUI

/// The voice list (#480): a search field, then one row per voice with ▶ to hear it, its name,
/// its description and a checkmark on the saved one. Clicking a row picks the voice; the
/// preview itself is made and played by the core (`VoicePreviewModel`).
struct VoicePickerView: View {
    let model: SettingsModel
    let spec: SettingsLayout.ControlSpec
    let items: [SettingsLayout.MenuItem]
    let selected: Int?
    let previews: VoicePreviewModel

    @State private var query = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            TextField("Search voices", text: $query)
                .textFieldStyle(.roundedBorder)
                .accessibilityIdentifier("voice.search")
            let shown = VoicePreviewModel.filter(items, query)
            ScrollViewReader { scroller in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 0) {
                        ForEach(shown, id: \.self) { index in
                            row(index)
                                .id(index)
                            if index != shown.last { Divider() }
                        }
                        if shown.isEmpty {
                            Text("No voice matches “\(query)”.")
                                .foregroundStyle(.secondary)
                                .padding(8)
                        }
                    }
                }
                .frame(height: 240)
                .onAppear { if let selected { scroller.scrollTo(selected, anchor: .center) } }
            }
            .background(RoundedRectangle(cornerRadius: 6).fill(Color(nsColor: .textBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color(nsColor: .separatorColor)))
        }
        .frame(width: 300)
        .onDisappear { previews.stop() }
    }

    private func voice(_ index: Int) -> String { items[index].value.stringValue ?? items[index].label }

    @ViewBuilder
    private func row(_ index: Int) -> some View {
        let item = items[index]
        let name = voice(index)
        let state = previews.state(name)
        let picked = index == selected
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 8) {
                Button { previews.toggle(name) } label: {
                    Group {
                        switch state {
                        case .loading: ProgressView().controlSize(.small)
                        case .playing: Image(systemName: "stop.fill")
                        case .idle, .failed: Image(systemName: "play.fill")
                        }
                    }
                    .frame(width: 18, height: 18)
                }
                .buttonStyle(.borderless)
                .disabled(!item.enabled)
                .help(state == .playing || state == .loading ? "Stop" : "Hear \(item.label)")
                .accessibilityLabel(state == .playing || state == .loading ? "Stop \(item.label)" : "Hear \(item.label)")
                .accessibilityIdentifier("voice.preview.\(name)")
                VStack(alignment: .leading, spacing: 0) {
                    Text(item.label).fontWeight(picked ? .semibold : .regular)
                    if let detail = item.detail, !detail.isEmpty {
                        Text(detail).font(.caption).foregroundStyle(.secondary)
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(picked ? [.isButton, .isSelected] : .isButton)
                .accessibilityAction { pick(index) }
                .accessibilityIdentifier("voice.row.\(name)")
                Spacer(minLength: 4)
                if picked {
                    Image(systemName: "checkmark").foregroundStyle(Color.accentColor)
                        .accessibilityLabel("Selected")
                }
            }
            if case .failed(let reason) = state {
                Text(reason).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 5)
        .background(picked ? Color.accentColor.opacity(0.12) : Color.clear)
        .contentShape(Rectangle())
        .onTapGesture { pick(index) }
    }

    private func pick(_ index: Int) {
        if items[index].enabled && index != selected { model.commit(spec.path, .menu(index)) }
    }
}
