import AppKit
import DeskPixieCore
import SwiftUI

/// The first-run window (#149), three steps: welcome; set up, a card per feature with its switch
/// and, while it is on, its setup; done. Every choice is a Settings control on the shared
/// `SettingsModel`, so it saves at once.
struct OnboardingView: View {
    @Bindable var state: OnboardingState
    let settings: SettingsModel
    let onFinish: () -> Void
    let onClose: () -> Void

    private var chosen: [OnboardingFeature] { OnboardingFlow.chosen(settings.snapshot?.values ?? .null) }

    var body: some View {
        let page = state.flow.page
        VStack(spacing: 0) {
            ScrollView {
                VStack(alignment: .leading, spacing: 14) {
                    content(page)
                    if let notice = settings.noticeText {
                        Text(notice).font(.callout).foregroundStyle(.red)
                    }
                }
                .padding(.horizontal, 32)
                .padding(.top, 24)
                .padding(.bottom, 12)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            Divider()
            footer(page)
        }
        .frame(width: OnboardingController.size.width, height: OnboardingController.size.height)
    }

    @ViewBuilder
    private func content(_ page: OnboardingPage) -> some View {
        switch page {
        case .welcome: welcome
        case .setup: setup
        case .done: done
        }
    }

    // MARK: Steps

    private var welcome: some View {
        VStack(alignment: .leading, spacing: 14) {
            Image(nsImage: NSApp.applicationIconImage)
                .resizable()
                .frame(width: 72, height: 72)
                .accessibilityHidden(true)
            Text("Meet Pixie")
                .font(.largeTitle.weight(.bold))
                .accessibilityAddTraits(.isHeader)
            Text("Pixie lives on your desktop and talks with you. It can hand real work to coding agents, and keep an eye on the Claude Code and Codex sessions you start yourself.")
                .fixedSize(horizontal: false, vertical: true)
            Text("Next, turn on what you want Pixie to do and get each part working. It takes a couple of minutes, and everything can be changed later in Settings.")
                .fixedSize(horizontal: false, vertical: true)
                .foregroundStyle(.secondary)
        }
        .padding(.top, 12)
    }

    private var setup: some View {
        let shown = OnboardingFlow.setupShown(chosen: chosen)
        return VStack(alignment: .leading, spacing: 12) {
            header("Set up Pixie")
            if settings.snapshot == nil {
                Text(settings.loadError.map { "Couldn't read settings: \($0)" } ?? "Loading…")
                    .foregroundStyle(.secondary)
            }
            ForEach(OnboardingFeature.allCases, id: \.self) { feature in
                card {
                    featureHeader(feature)
                    if shown.contains(feature) {
                        Divider()
                        switch feature {
                        case .voice: voiceSetup
                        case .agents: agentsSetup
                        case .sessions: sessionsSetup
                        case .canvas: EmptyView()
                        }
                    }
                }
            }
            Text("Each switch is the same setting as in Settings, and saves as you flip it.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }

    /// The card's top row: the feature, what it does, and its switch ("Always on" for voice).
    private func featureHeader(_ feature: OnboardingFeature) -> some View {
        HStack(alignment: .center, spacing: 12) {
            VStack(alignment: .leading, spacing: 3) {
                Text(feature.title).font(.headline)
                Text(feature.summary)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            if let path = feature.settingPath {
                if let spec = settings.spec(path: path) {
                    SettingsControlView(model: settings, spec: spec)
                        .disabled(!settings.enabled)
                        .accessibilityLabel(feature.title)
                }
            } else {
                Text("Always on").font(.callout).foregroundStyle(.secondary)
            }
        }
        .padding(.vertical, 6)
    }

    @ViewBuilder
    private var voiceSetup: some View {
        let provider = settings.snapshot?.values.at("voice.provider")?.stringValue
        let id = OnboardingChecks.secretID(forProvider: provider)
        let checks = state.checks(.voice, settings: settings)
        field("voice.provider", label: "Voice service")
        VStack(alignment: .leading, spacing: 6) {
            checkRow(checks[0])
            if !checks[0].ok {
                HStack(spacing: 6) {
                    SecureField("Paste key", text: Binding(get: { settings.keyDraft(id) },
                                                           set: { settings.setKeyDraft(id, $0) }))
                        .onSubmit { settings.saveKey(id) }
                        .accessibilityLabel(id.label)
                    Button("Save") { settings.saveKey(id) }
                        .disabled(!settings.canSaveKey(id))
                }
                if let error = settings.keyError(id) {
                    Text(error).font(.callout).foregroundStyle(.red)
                }
            }
        }
        checkRow(checks[1])
        field("hotkey", label: "Voice hotkey")
        Text("Double-clicking Pixie starts a conversation too.")
            .font(.callout)
            .foregroundStyle(.secondary)
    }

    @ViewBuilder
    private var agentsSetup: some View {
        let lines = OnboardingChecks.coordinatorLines(state.statusLines)
        let host = OnboardingChecks.sessionHostName(settings.snapshot?.values ?? .null)
        Text("The coordinator is an agent in a \(host) session that runs workers for you.")
            .font(.callout)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
        field("agent.coordinatorKind", label: "Coordinator agent")
        ForEach(Array(state.checks(.agents, settings: settings).enumerated()), id: \.offset) { _, check in
            checkRow(check)
        }
        ForEach(lines, id: \.self) { line in
            Label(line, systemImage: "exclamationmark.triangle.fill")
                .font(.callout)
                .foregroundStyle(.orange)
        }
        HStack {
            Spacer()
            Button("Check Again") { state.refreshPrograms() }
        }
    }

    @ViewBuilder
    private var sessionsSetup: some View {
        Text("Start Claude Code or Codex with pix in any terminal (pix claude, pix codex).")
            .font(.callout)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
        ForEach(Array(state.checks(.sessions, settings: settings).enumerated()), id: \.offset) { _, check in
            checkRow(check)
        }
        if let pix = state.pix {
            if let error = pix.error {
                Text(error).font(.callout).foregroundStyle(.red)
            }
            if pix.state == .installed && !pix.onPath {
                HStack {
                    Text(PixInstaller.pathLine).font(.callout.monospaced()).textSelection(.enabled)
                    Spacer()
                    copyButton("Copy Line", PixInstaller.pathLine)
                }
            }
        }
        HStack {
            Link("How pix works", destination: URL(string: "https://github.com/sihekuang/deskpixie/blob/main/docs/pix.md")!)
                .font(.callout)
            Spacer()
            copyButton("Copy Aliases", PixInstaller.aliases)
                .help("Copies alias lines for ~/.zshrc, so plain claude and codex run through pix.")
        }
    }

    private var done: some View {
        let checks = Dictionary(uniqueKeysWithValues: chosen.map { ($0, state.checks($0, settings: settings)) })
        let lines = OnboardingChecks.summary(chosen: chosen, checks: checks)
        return VStack(alignment: .leading, spacing: 12) {
            header(lines.allSatisfy(\.ok) ? "You're all set" : "Almost there")
            card {
                ForEach(Array(lines.enumerated()), id: \.offset) { i, line in
                    if i > 0 { Divider() }
                    checkRow(OnboardingCheck(title: line.feature.title, ok: line.ok, detail: line.detail))
                }
            }
            if !lines.allSatisfy(\.ok) {
                Text("You can finish now and come back any time: Welcome to DeskPixie… in the menu bar, or Settings.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            if settings.restartText != nil {
                Text("Some of these start after a restart, so DeskPixie restarts when you finish.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
    }

    // MARK: Parts

    private static func stepName(_ page: OnboardingPage) -> String {
        switch page {
        case .welcome: return "Welcome"
        case .setup: return "Set up"
        case .done: return "Done"
        }
    }

    private func footer(_ page: OnboardingPage) -> some View {
        HStack {
            Group {
                if state.flow.canGoBack {
                    Button("Back") { state.flow.back() }
                } else {
                    Button("Not Now") { onClose() }
                }
            }
            .frame(width: 140, alignment: .leading)
            Spacer()
            HStack(spacing: 6) {
                ForEach(Array(OnboardingFlow.pages.enumerated()), id: \.offset) { i, step in
                    if i > 0 { Text("·").foregroundStyle(.tertiary) }
                    Text(Self.stepName(step))
                        .font(.callout.weight(step == page ? .semibold : .regular))
                        .foregroundStyle(step == page ? Color.accentColor : Color.secondary)
                }
            }
            .accessibilityElement()
            .accessibilityLabel("Step \(state.flow.index + 1) of \(OnboardingFlow.pages.count): \(Self.stepName(page))")
            Spacer()
            Group {
                if page == .done {
                    Button(OnboardingFlow.finishTitle(restartPending: settings.restartText != nil)) { onFinish() }
                } else {
                    Button(page == .welcome ? "Get Started" : "Continue") { state.flow.next() }
                }
            }
            .keyboardShortcut(.defaultAction)
            .frame(width: 140, alignment: .trailing)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 14)
    }

    private func header(_ text: String) -> some View {
        Text(text)
            .font(.title2.weight(.bold))
            .accessibilityAddTraits(.isHeader)
    }

    private func card<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) { content() }
            .padding(.horizontal, 14)
            .padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 10).fill(Color(nsColor: .controlBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(.separator))
    }

    /// A Settings field by path, label left and the Settings control right.
    @ViewBuilder
    private func field(_ path: String, label: String) -> some View {
        if let spec = settings.spec(path: path) {
            HStack {
                Text(label).font(.headline)
                Spacer()
                SettingsControlView(model: settings, spec: spec)
                    .disabled(!settings.enabled)
            }
            if let error = spec.note.isError ? spec.note.text : nil {
                Text(error).font(.callout).foregroundStyle(.red)
            }
        }
    }

    private func checkRow(_ check: OnboardingCheck) -> some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: check.ok ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                .foregroundStyle(check.ok ? Color.green : Color.orange)
                .font(.title3)
                .accessibilityLabel(check.ok ? "Ready" : "Needs attention")
            VStack(alignment: .leading, spacing: 2) {
                Text(check.title).font(.headline)
                Text(check.detail)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            if let fix = check.fix { fixButton(fix) }
        }
    }

    @ViewBuilder
    private func fixButton(_ fix: OnboardingCheck.Fix) -> some View {
        switch fix {
        case .allowMicrophone:
            Button("Allow Microphone") { state.requestMic() }
        case .openMicrophoneSettings:
            Button("Open Privacy Settings") { state.openMicSettings() }
        case .installPix:
            Button("Install") {
                state.pix?.install()
                state.refreshPix()
            }
            .disabled(state.pix == nil)
        case .link(let title, let url):
            Link(title, destination: url)
        }
    }

    private func copyButton(_ title: String, _ text: String) -> some View {
        Button(title) {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString(text, forType: .string)
        }
    }
}
