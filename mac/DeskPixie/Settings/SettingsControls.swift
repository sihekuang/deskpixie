import AppKit
import DeskPixieCore
import SwiftUI

/// The control for one field (`SettingsLayout.Control`): switch, pop-up, number field with
/// stepper, checkboxes, text, path with Choose…/Clear, or the hotkey recorder. Every change
/// goes through `SettingsModel`; text commits on Return or when the field loses focus.
struct SettingsControlView: View {
    let model: SettingsModel
    let spec: SettingsLayout.ControlSpec

    var body: some View {
        Group {
            switch spec.control {
            case .switch(let on):
                Toggle(spec.a11yLabel, isOn: Binding(get: { on }, set: { model.commit(spec.path, .switch($0)) }))
                    .toggleStyle(.switch)
                    .labelsHidden()
            case .menu(let items, let selected):
                Picker(spec.a11yLabel, selection: Binding(
                    get: { selected ?? -1 },
                    set: { model.commit(spec.path, .menu($0)) }
                )) {
                    if selected == nil {
                        Text("").tag(-1)
                    }
                    ForEach(Array(items.enumerated()), id: \.offset) { index, item in
                        Text(item.label).tag(index).disabled(!item.enabled)
                    }
                }
                .labelsHidden()
                .pickerStyle(.menu)
                .fixedSize()
            case .stepper(let min, let max, let value, _):
                HStack(spacing: 4) {
                    CommitTextField(model: model, spec: spec, width: 80)
                    Stepper(spec.a11yLabel, value: Binding(
                        get: { value },
                        set: { model.commit(spec.path, .step($0)) }
                    ), in: min...max)
                    .labelsHidden()
                }
            case .checks(let items, _):
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(items.enumerated()), id: \.offset) { index, item in
                        Toggle(item.label, isOn: Binding(
                            get: { item.checked },
                            set: { model.commit(spec.path, .check(index: index, on: $0)) }
                        ))
                        .toggleStyle(.checkbox)
                        .disabled(!item.enabled)
                    }
                }
            case .text:
                CommitTextField(model: model, spec: spec, width: 260)
            case .path(let path, let dir, _):
                PathControl(model: model, spec: spec, path: path, dir: dir)
            case .hotkey(_, let title):
                HotkeyRecorderButton(model: model, path: spec.path, title: title, label: spec.a11yLabel)
            }
        }
        .accessibilityLabel(spec.a11yLabel)
        .accessibilityHint(spec.help)
    }
}

/// A text or number field that commits on Return and when it loses focus.
private struct CommitTextField: View {
    let model: SettingsModel
    let spec: SettingsLayout.ControlSpec
    let width: CGFloat

    @FocusState private var focused: Bool

    var body: some View {
        TextField(spec.a11yLabel, text: Binding(
            get: { model.text(for: spec) },
            set: { model.setEditing(spec.path, $0) }
        ))
        .labelsHidden()
        .textFieldStyle(.roundedBorder)
        .frame(width: width)
        .focused($focused)
        .onSubmit { model.commitText(spec.path) }
        .onChange(of: focused) { _, isFocused in
            if !isFocused { model.commitText(spec.path) }
        }
    }
}

/// A path with Choose… (an open panel for a file, or a folder for `dir`) and Clear.
private struct PathControl: View {
    let model: SettingsModel
    let spec: SettingsLayout.ControlSpec
    let path: String?
    let dir: Bool

    var body: some View {
        HStack(spacing: 6) {
            Text(model.drafts[spec.path] ?? path ?? "None")
                .foregroundStyle(path == nil && model.drafts[spec.path] == nil ? .secondary : .primary)
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: 220, alignment: .leading)
                .help(path ?? "")
            Button("Choose…", action: choose)
            Button("Clear") { model.commit(spec.path, .path(nil)) }
        }
    }

    private func choose() {
        model.commitPendingEdits()
        let panel = NSOpenPanel()
        panel.canChooseDirectories = dir
        panel.canChooseFiles = !dir
        panel.allowsMultipleSelection = false
        panel.title = spec.a11yLabel
        if let path, SettingsLayout.isAbsolutePath(path) {
            let url = URL(fileURLWithPath: path)
            panel.directoryURL = dir ? url : url.deletingLastPathComponent()
        }
        guard panel.runModal() == .OK, let picked = panel.url?.path else { return }
        model.commit(spec.path, .path(picked))
    }
}

/// The hotkey recorder: click, then type a shortcut. It needs ⌃, ⌥ or ⌘ unless it is a
/// function key; Esc or a click elsewhere cancels. The shortcut is bound before it is saved.
private struct HotkeyRecorderButton: View {
    let model: SettingsModel
    let path: String
    let title: String
    let label: String

    @State private var monitor = KeyMonitor()

    private var recording: Bool { model.recordingPath == path }

    var body: some View {
        Button(recording ? SettingsLayout.recording : (title.isEmpty ? "None" : title)) {
            if recording { stop() } else { start() }
        }
        .frame(minWidth: 140)
        .help("Click, then type a shortcut. Esc cancels.")
        .onDisappear { stop() }
    }

    private func start() {
        model.commitPendingEdits()
        model.recordingPath = path
        monitor.start { event in
            switch event.type {
            case .keyDown:
                let flags = UInt64(event.modifierFlags.rawValue)
                switch HotkeyRecorder.record(keyCode: event.keyCode, flags: flags) {
                case .combo(let combo):
                    stop()
                    model.recordHotkey(path, combo)
                case .cancel:
                    stop()
                case .refused:
                    NSSound.beep()
                case .ignored:
                    break
                }
                return true
            default:
                // A click anywhere but the recorder cancels and goes through; a click on the
                // recorder reaches its action, which stops recording.
                if !monitor.isOnRecorder(event) { stop() }
                return false
            }
        }
    }

    private func stop() {
        monitor.stop()
        if model.recordingPath == path { model.recordingPath = nil }
    }
}

/// A local NSEvent monitor for key-downs and clicks, removed on stop.
@MainActor
private final class KeyMonitor {
    private var token: Any?
    private var startedInWindow: NSWindow?
    private var startedAt: NSPoint?

    /// `handler` returns whether the event is consumed.
    func start(_ handler: @escaping @MainActor (NSEvent) -> Bool) {
        stop()
        startedInWindow = NSApp.keyWindow
        startedAt = NSApp.currentEvent.map { $0.locationInWindow }
        token = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .leftMouseDown, .rightMouseDown, .otherMouseDown]) { event in
            // Local monitors run on the main thread.
            nonisolated(unsafe) let e = event
            let swallow = MainActor.assumeIsolated { handler(e) }
            return swallow ? nil : event
        }
    }

    /// Whether a click lands on the recorder button that started recording (the click that
    /// started it was at `startedAt`, inside the button).
    func isOnRecorder(_ event: NSEvent) -> Bool {
        guard event.window === startedInWindow, let startedAt,
              let view = event.window?.contentView?.hitTest(event.locationInWindow),
              let origin = event.window?.contentView?.hitTest(startedAt) else { return false }
        return view === origin
    }

    func stop() {
        if let token { NSEvent.removeMonitor(token) }
        token = nil
    }
}
