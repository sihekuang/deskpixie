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
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 0) {
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
                .padding(.leading, 8)
                .padding(.trailing, 4)
                .disabled(!item.enabled)
                .help(state == .playing || state == .loading ? "Stop" : "Hear \(item.label)")
                .accessibilityLabel(state == .playing || state == .loading ? "Stop \(item.label)" : "Hear \(item.label)")
                .accessibilityIdentifier("voice.preview.\(name)")
                // The rest of the row is one plain button: a click anywhere on it picks the voice
                // (a tap gesture lost clicks that landed on the name's text).
                Button { pick(index) } label: {
                    HStack(spacing: 8) {
                        VStack(alignment: .leading, spacing: 0) {
                            Text(item.label).fontWeight(picked ? .semibold : .regular)
                            if let detail = item.detail, !detail.isEmpty {
                                Text(detail).font(.caption).foregroundStyle(.secondary)
                            }
                        }
                        Spacer(minLength: 4)
                        if picked {
                            Image(systemName: "checkmark").foregroundStyle(Color.accentColor)
                        }
                    }
                    // The row's margins are inside the button, so a click there picks too.
                    .padding(.leading, 4)
                    .padding(.trailing, 8)
                    .padding(.vertical, 5)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(!item.enabled)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(picked ? .isSelected : [])
                .accessibilityIdentifier("voice.row.\(name)")
            }
            if case .failed(let reason) = state {
                Text(reason).font(.caption).foregroundStyle(.red).fixedSize(horizontal: false, vertical: true)
                    .padding(.horizontal, 8)
                    .padding(.bottom, 5)
            }
        }
        .background(picked ? Color.accentColor.opacity(0.12) : Color.clear)
    }

    private func pick(_ index: Int) {
        if items[index].enabled && index != selected { model.commit(spec.path, .menu(index)) }
    }
}
