import AppKit
import SwiftUI

// Building blocks for the Settings window, after Howl's `SettingsComponents.swift`: a hook to
// the hosting NSWindow (frame autosave, focus reloads, activation policy), notes and strips.

/// Calls `onWindow` once with the NSWindow hosting this view.
struct WindowAccessor: NSViewRepresentable {
    let onWindow: (NSWindow) -> Void

    func makeNSView(context: Context) -> NSView {
        let view = HostView()
        view.onWindow = onWindow
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {}

    final class HostView: NSView {
        var onWindow: ((NSWindow) -> Void)?

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard let window, let onWindow else { return }
            self.onWindow = nil
            onWindow(window)
        }
    }
}

/// A red (error) or secondary (help) line under a control.
struct NoteText: View {
    let text: String
    let isError: Bool

    var body: some View {
        Text(text)
            .font(.subheadline)
            .foregroundStyle(isError ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: 360, alignment: .leading)
            .accessibilityAddTraits(isError ? .updatesFrequently : [])
    }
}

/// A full-width strip with an optional symbol, wrapping text and a trailing button: the
/// file-error banner and the Restart bar.
struct SettingsStrip: View {
    let symbol: String?
    let text: String
    let buttonTitle: String
    let buttonHelp: String
    let action: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 8) {
            if let symbol {
                Image(systemName: symbol)
                    .foregroundStyle(.yellow)
                    .accessibilityHidden(true)
            }
            Text(text)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            Button(buttonTitle, action: action)
                .help(buttonHelp)
        }
        .padding(10)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
    }
}
