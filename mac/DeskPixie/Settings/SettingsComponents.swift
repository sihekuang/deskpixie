import AppKit
import DeskPixieCore
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

/// The bar pinned above a Settings page while a change waits for a restart (#440): what to do,
/// what changed (two lines at most), and a prominent Restart Now.
struct RestartBar: View {
    let changed: String
    let restart: () -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: "arrow.clockwise.circle.fill")
                .font(.title3)
                .foregroundStyle(.orange)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 2) {
                Text(SettingsLayout.restartTitle)
                    .fontWeight(.semibold)
                Text(changed)
                    .font(.subheadline)
                    .lineLimit(2)
                    .truncationMode(.tail)
                    .foregroundStyle(.secondary)
            }
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityElement(children: .combine)
            Button("Restart Now", action: restart)
                .buttonStyle(.borderedProminent)
                .help("Restarts DeskPixie to apply the changes.")
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 9)
        .background(.orange.opacity(0.15), in: RoundedRectangle(cornerRadius: 9, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 9, style: .continuous).strokeBorder(.orange.opacity(0.45)))
        .background(.background, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
        .shadow(color: .black.opacity(0.25), radius: 6, y: 3)
    }
}

/// A full-width strip with an optional symbol, wrapping text and a trailing button: the
/// file-error banner.
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
