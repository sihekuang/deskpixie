import AppKit
import DeskPixieCore
import SwiftUI

/// The first-run window's pages (#149): welcome, choose, a setup page per chosen feature, done.
/// Every choice is a Settings control on the shared `SettingsModel`, so it saves at once.
struct OnboardingView: View {
    @Bindable var state: OnboardingState
    let settings: SettingsModel
    let onFinish: () -> Void
    let onClose: () -> Void

    private var chosen: [OnboardingFeature] { OnboardingFlow.chosen(settings.snapshot?.values ?? .null) }
    private var pages: [OnboardingPage] { OnboardingFlow.pages(chosen: chosen) }

    var body: some View {
        let pages = pages
        let page = state.flow.page(in: pages)
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
            footer(pages: pages, page: page)
        }
        .frame(width: OnboardingController.size.width, height: OnboardingController.size.height)
    }

    @ViewBuilder
    private func content(_ page: OnboardingPage) -> some View {
        switch page {
        case .welcome: welcome
        case .choose: choose
        case .setup(.voice): voice
        case .setup(.agents): agents
        case .setup(.sessions): sessions
        case .setup(.canvas): EmptyView()
        case .done: done
        }
    }

    // MARK: Pages

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
            Text("Pick what you want Pixie to do, and get each part working. It takes a couple of minutes, and everything can be changed later in Settings.")
                .fixedSize(horizontal: false, vertical: true)
                .foregroundStyle(.secondary)
        }
        .padding(.top, 12)
    }

    private var choose: some View {
        VStack(alignment: .leading, spacing: 12) {
            header("What should Pixie do?")
            card {
                ForEach(OnboardingFeature.allCases, id: \.self) { feature in
                    HStack(alignment: .center, spacing: 12) {
                        VStack(alignment: .leading, spacing: 3) {
                            Text(feature.title).font(.headline)
                            Text(feature.summary)
                                .font(.callout)
                                .foregroundStyle(.secondary)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        Spacer(minLength: 8)
                        if let path = feature.settingPath, let spec = settings.spec(path: path) {
                            SettingsControlView(model: settings, spec: spec)
                                .disabled(!settings.enabled)
                                .accessibilityLabel(feature.title)
                        } else {
                            Text("Always on").font(.callout).foregroundStyle(.secondary)
                        }
                    }
                    .padding(.vertical, 6)
                    if feature != OnboardingFeature.allCases.last { Divider() }
                }
            }
            Text("Each switch is the same setting as in Settings, and saves as you flip it.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }

    private var voice: some View {
        let provider = settings.snapshot?.values.at("voice.provider")?.stringValue
        let id = OnboardingChecks.secretID(forProvider: provider)
        let checks = state.checks(.voice, settings: settings)
        return VStack(alignment: .leading, spacing: 12) {
            header(OnboardingFeature.voice.title)
            card {
                field("voice.provider", label: "Voice service")
                Divider()
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
                .padding(.vertical, 4)
                Divider()
                checkRow(checks[1])
                Divider()
                field("hotkey", label: "Voice hotkey")
                Text("Double-clicking Pixie starts a conversation too.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var agents: some View {
        let lines = OnboardingChecks.coordinatorLines(state.statusLines)
        return VStack(alignment: .leading, spacing: 12) {
            header(OnboardingFeature.agents.title)
            Text("When you ask for real work, Pixie hands it to the coordinator, an agent in a herdr session that runs workers for you.")
                .fixedSize(horizontal: false, vertical: true)
                .foregroundStyle(.secondary)
            card {
                field("agent.coordinatorKind", label: "Coordinator agent")
                ForEach(Array(state.checks(.agents, settings: settings).enumerated()), id: \.offset) { _, check in
                    Divider()
                    checkRow(check)
                }
                Divider()
                HStack {
                    Spacer()
                    Button("Check Again") { state.refreshPrograms() }
                }
                .padding(.vertical, 4)
            }
            ForEach(lines, id: \.self) { line in
                Label(line, systemImage: "exclamationmark.triangle.fill")
                    .font(.callout)
                    .foregroundStyle(.orange)
            }
        }
    }

    private var sessions: some View {
        VStack(alignment: .leading, spacing: 12) {
            header(OnboardingFeature.sessions.title)
            Text("Start Claude Code or Codex with pix in any terminal (pix claude, pix codex). Pixie tells you when a turn finishes, shows their questions in its bubble, and can message them.")
                .fixedSize(horizontal: false, vertical: true)
                .foregroundStyle(.secondary)
            card {
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
                Divider()
                HStack {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Aliases").font(.headline)
                        Text("Make claude and codex start through pix every time.")
                            .font(.callout)
                            .foregroundStyle(.secondary)
                    }
                    Spacer()
                    copyButton("Copy Aliases", PixInstaller.aliases)
                }
                .padding(.vertical, 4)
            }
            Link("How pix works", destination: URL(string: "https://github.com/sihekuang/deskpixie/blob/main/docs/pix.md")!)
                .font(.callout)
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

    private func footer(pages: [OnboardingPage], page: OnboardingPage) -> some View {
        let index = pages.firstIndex(of: page) ?? 0
        return HStack {
            Group {
                if state.flow.canGoBack {
                    Button("Back") { state.flow.back(in: pages) }
                } else {
                    Button("Not Now") { onClose() }
                }
            }
            .frame(width: 140, alignment: .leading)
            Spacer()
            HStack(spacing: 7) {
                ForEach(pages.indices, id: \.self) { i in
                    Circle()
                        .fill(i == index ? Color.accentColor : Color.secondary.opacity(0.35))
                        .frame(width: 7, height: 7)
                }
            }
            .accessibilityElement()
            .accessibilityLabel("Step \(index + 1) of \(pages.count)")
            Spacer()
            Group {
                if page == .done {
                    Button(OnboardingFlow.finishTitle(restartPending: settings.restartText != nil)) { onFinish() }
                } else {
                    Button(page == .welcome ? "Get Started" : "Continue") { state.flow.next(in: pages) }
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
        VStack(alignment: .leading, spacing: 6) { content() }
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
            .padding(.vertical, 4)
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
        .padding(.vertical, 4)
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
