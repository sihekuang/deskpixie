import AppKit
import DeskPixieCore
import SwiftUI

/// The first-run window (#149), three steps: welcome; set up, a card per feature with its switch
/// and, while it is on, its setup; done. Every choice is a Settings control on the shared
/// `SettingsModel`, so it saves at once. Pixie itself greets on the first step and cheers on the
/// last, in Pixie's green.
struct OnboardingView: View {
    @Bindable var state: OnboardingState
    let settings: SettingsModel
    let onFinish: () -> Void
    let onClose: () -> Void

    /// Pixie's body green (the pack's sheet), for washes and icons.
    static let pixieGreen = Color(red: 0, green: 0.76, blue: 0.58)
    /// A darker green that keeps white button text readable.
    static let accent = Color(red: 0, green: 0.50, blue: 0.37)

    private var chosen: [OnboardingFeature] { OnboardingFlow.chosen(settings.snapshot?.values ?? .null) }

    var body: some View {
        let page = state.flow.page
        VStack(spacing: 0) {
            switch page {
            case .welcome: welcome
            case .setup: scrolling { setup }
            case .permissions: scrolling { permissions }
            case .done: scrolling { done }
            }
            if page != .welcome {
                footer(page)
            }
        }
        .frame(width: OnboardingController.size.width, height: OnboardingController.size.height)
        .background(background(page))
        .tint(Self.accent)
    }

    private func background(_ page: OnboardingPage) -> some View {
        ZStack {
            Color(nsColor: .windowBackgroundColor)
            if page != .setup {
                RadialGradient(colors: [Self.pixieGreen.opacity(0.22), .clear],
                               center: .top, startRadius: 0, endRadius: 420)
            }
        }
        .ignoresSafeArea()
    }

    private func scrolling<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                content()
                if let notice = settings.noticeText {
                    Text(notice).font(.callout).foregroundStyle(.red)
                }
            }
            .padding(.horizontal, 28)
            .padding(.top, 20)
            .padding(.bottom, 16)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    // MARK: Welcome

    private var welcome: some View {
        VStack(spacing: 0) {
            pixie("idle")
            Text("Meet Pixie")
                .font(.system(size: 30, weight: .bold))
                .padding(.top, 18)
                .accessibilityAddTraits(.isHeader)
            Text("A small companion on your desktop that you talk to. It can take on real work and keep watch over your coding sessions.")
                .font(.body)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 420)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.top, 8)
            VStack(alignment: .leading, spacing: 16) {
                highlight(.voice, "Talk from any app", "Press your hotkey or double-click Pixie, and just speak.")
                highlight(.agents, "Hand off real work", "Pixie passes tasks to coding agents and tells you how they went.")
                highlight(.sessions, "Watch your sessions", "Start Claude Code or Codex with pix, and Pixie keeps an eye on them.")
            }
            .frame(width: 420, alignment: .leading)
            .padding(.top, 28)
            Spacer(minLength: 16)
            Button { state.flow.next() } label: {
                Text("Get Started").frame(width: 220)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .keyboardShortcut(.defaultAction)
            Button("Not Now") { onClose() }
                .buttonStyle(.borderless)
                .foregroundStyle(.secondary)
                .padding(.top, 8)
            Text("About two minutes. Everything can be changed later in Settings.")
                .font(.caption)
                .foregroundStyle(.tertiary)
                .padding(.top, 6)
        }
        .padding(.horizontal, 40)
        .padding(.top, 48)
        .padding(.bottom, 24)
        .frame(maxWidth: .infinity)
    }

    private func highlight(_ feature: OnboardingFeature, _ title: String, _ text: String) -> some View {
        HStack(alignment: .top, spacing: 14) {
            tile(feature, size: 36)
            VStack(alignment: .leading, spacing: 2) {
                Text(title).font(.headline)
                Text(text)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }

    // MARK: Set up

    private var setup: some View {
        let shown = OnboardingFlow.setupShown(chosen: chosen)
        return VStack(alignment: .leading, spacing: 12) {
            VStack(alignment: .leading, spacing: 4) {
                Text("Set up Pixie")
                    .font(.title2.weight(.bold))
                    .accessibilityAddTraits(.isHeader)
                Text("Turn on what you'd like. Each part checks itself as you go.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
            }
            .padding(.horizontal, 4)
            .padding(.top, 16)
            if settings.snapshot == nil {
                Text(settings.loadError.map { "Couldn't read settings: \($0)" } ?? "Loading…")
                    .foregroundStyle(.secondary)
            }
            ForEach(OnboardingFeature.allCases, id: \.self) { feature in
                card {
                    featureHeader(feature, on: chosen.contains(feature))
                    if shown.contains(feature) {
                        VStack(alignment: .leading, spacing: 10) {
                            switch feature {
                            case .voice: voiceSetup
                            case .agents: agentsSetup
                            case .sessions: sessionsSetup
                            case .canvas: EmptyView()
                            }
                        }
                        .padding(.leading, 56)
                        .padding(.trailing, 14)
                        .padding(.vertical, 12)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color.primary.opacity(0.03))
                        .overlay(alignment: .top) { Divider() }
                    }
                }
            }
            Text("Each switch is the same setting as in Settings, and saves as you flip it.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 4)
        }
    }

    /// The card's top row: icon, the feature and what it does, its status, and its switch
    /// ("Always on" for voice).
    private func featureHeader(_ feature: OnboardingFeature, on: Bool) -> some View {
        HStack(alignment: .center, spacing: 12) {
            tile(feature, size: 30)
            VStack(alignment: .leading, spacing: 2) {
                Text(feature.title).font(.headline)
                Text(feature.summary)
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 8)
            if on, let status = OnboardingChecks.status(state.checks(feature, settings: settings)) {
                statusPill(status)
            }
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
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
    }

    private func statusPill(_ status: String) -> some View {
        let ready = status == "Ready"
        return Text(status)
            .font(.caption.weight(.semibold))
            .foregroundStyle(ready ? Self.accent : Color.orange)
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(Capsule().fill((ready ? Self.pixieGreen : Color.orange).opacity(0.16)))
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
            .font(.caption)
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
                .controlSize(.small)
        }
    }

    /// Install pix, start an agent with it, optionally always use it, and what Pixie then does.
    @ViewBuilder
    private var sessionsSetup: some View {
        let pixCheck = state.checks(.sessions, settings: settings).first
        step(1, "Install the pix command", done: pixCheck?.ok == true) {
            if let pixCheck {
                // The step names it already: just its state and the fix.
                HStack(spacing: 8) {
                    Image(systemName: pixCheck.ok ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                        .foregroundStyle(pixCheck.ok ? Self.pixieGreen : Color.orange)
                        .accessibilityLabel(pixCheck.ok ? "Ready" : "Needs attention")
                    Text(pixCheck.detail)
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 8)
                    if let fix = pixCheck.fix { fixButton(fix) }
                }
            }
            if let pix = state.pix {
                if let error = pix.error {
                    Text(error).font(.callout).foregroundStyle(.red)
                }
                if pix.state == .installed && !pix.onPath {
                    HStack {
                        Text(PixInstaller.pathLine).font(.caption.monospaced()).textSelection(.enabled)
                        Spacer()
                        copyButton("Copy Line", PixInstaller.pathLine)
                    }
                }
            }
        }
        step(2, "Start your agent with pix", done: false) {
            Text("In any terminal, run it instead of claude or codex. It works exactly the same, and Pixie can now see it.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: 6) {
                ForEach(OnboardingChecks.pixCommands, id: \.self) { command in
                    HStack {
                        Text("$ ").foregroundStyle(.white.opacity(0.45)) + Text(command).foregroundStyle(.white)
                        Spacer()
                        copyButton("Copy", command)
                            .controlSize(.small)
                    }
                    .font(.callout.monospaced())
                }
            }
            .padding(10)
            .background(RoundedRectangle(cornerRadius: 8).fill(Color(white: 0.13)))
            .environment(\.colorScheme, .dark)
        }
        step(3, "Optional: always use pix", done: false) {
            HStack(alignment: .center) {
                Text("Aliases make plain claude and codex start through pix. Paste them into ~/.zshrc.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 8)
                copyButton("Copy Aliases", PixInstaller.aliases)
            }
        }
        VStack(alignment: .leading, spacing: 5) {
            Text("Then Pixie:").font(.callout.weight(.semibold))
            bullet("tells you when a turn finishes")
            bullet("shows the agent's questions in its bubble, so you can answer from anywhere")
            bullet("takes requests like \u{201C}tell the login-bug one to stop\u{201D}")
            Link("How pix works", destination: URL(string: "https://github.com/sihekuang/deskpixie/blob/main/docs/pix.md")!)
                .font(.callout)
                .padding(.top, 2)
        }
    }

    private func step<Content: View>(_ number: Int, _ title: String, done: Bool,
                                     @ViewBuilder _ content: () -> Content) -> some View {
        HStack(alignment: .top, spacing: 10) {
            ZStack {
                Circle().fill(done ? Self.accent : Color.secondary.opacity(0.18))
                if done {
                    Image(systemName: "checkmark").font(.caption2.weight(.bold)).foregroundStyle(.white)
                } else {
                    Text("\(number)").font(.caption.weight(.semibold)).foregroundStyle(.secondary)
                }
            }
            .frame(width: 20, height: 20)
            .accessibilityLabel(done ? "Step \(number), done" : "Step \(number)")
            VStack(alignment: .leading, spacing: 6) {
                Text(title).font(.callout.weight(.semibold))
                content()
            }
        }
    }

    private func bullet(_ text: String) -> some View {
        Label {
            Text(text).font(.callout).foregroundStyle(.secondary)
        } icon: {
            Image(systemName: "checkmark").font(.caption.weight(.semibold)).foregroundStyle(Self.accent)
        }
    }

    // MARK: Permissions

    /// Every permission DeskPixie uses, asked for now so nothing stops you later (#210).
    private var permissions: some View {
        let checks = state.permissionChecks()
        let open = state.openPermissions
        return VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .bottom) {
                VStack(alignment: .leading, spacing: 4) {
                    Text("Allow permissions")
                        .font(.title2.weight(.bold))
                        .accessibilityAddTraits(.isHeader)
                    Text("macOS asks once for each. Allow them now and Pixie won't stop to ask while you work.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 12)
                Button(state.asking ? "Asking…" : "Allow All") { state.askAll() }
                    .buttonStyle(.borderedProminent)
                    .disabled(state.asking || open.isEmpty)
            }
            .padding(.horizontal, 4)
            .padding(.top, 16)
            card {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(zip(state.permissions, checks).enumerated()), id: \.offset) { i, row in
                        if i > 0 { Divider() }
                        checkRow(row.1, purpose: row.0.purpose)
                            .padding(.vertical, 9)
                    }
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 2)
            }
            Text("Each one can be changed later in System Settings ▸ Privacy & Security.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.horizontal, 4)
        }
    }

    // MARK: Done

    private var done: some View {
        let checks = Dictionary(uniqueKeysWithValues: chosen.map { ($0, state.checks($0, settings: settings)) })
        let lines = OnboardingChecks.summary(chosen: chosen, checks: checks)
        let permissions = OnboardingChecks.permissionsSummary(state.permissionChecks())
        let allOk = lines.allSatisfy(\.ok) && permissions.ok
        return VStack(spacing: 0) {
            pixie(allOk ? "happy" : "idle")
            Text(allOk ? "You're all set" : "Almost there")
                .font(.system(size: 26, weight: .bold))
                .padding(.top, 14)
                .accessibilityAddTraits(.isHeader)
            Text(allOk ? "Pixie is ready on your desktop." : "Finish now and come back any time: Welcome to DeskPixie… in the menu bar.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .padding(.top, 4)
            card {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { i, line in
                        if i > 0 { Divider() }
                        checkRow(OnboardingCheck(title: line.feature.title, ok: line.ok, detail: line.detail))
                            .padding(.vertical, 9)
                    }
                    Divider()
                    checkRow(permissions)
                        .padding(.vertical, 9)
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 2)
            }
            .padding(.top, 18)
            tryIt
                .padding(.top, 12)
            if settings.restartText != nil {
                Text("Some choices start after a restart, so DeskPixie restarts when you finish.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, 10)
            }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 8)
    }

    /// Something to do right away: say hello, and start a pix session when sessions are on.
    private var tryIt: some View {
        let caps = OnboardingChecks.hotkeyCaps(settings.snapshot?.values.at("hotkey")?.stringValue)
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 6) {
                Text("Try it:").font(.callout.weight(.semibold))
                Text("press").font(.callout)
                ForEach(caps, id: \.self) { keycap($0) }
                Text("and say hello").font(.callout)
            }
            .accessibilityElement(children: .combine)
            if chosen.contains(.sessions) {
                HStack(spacing: 6) {
                    Text("Then run").font(.callout)
                    Text(OnboardingChecks.pixCommands[0])
                        .font(.callout.monospaced())
                        .padding(.horizontal, 6)
                        .padding(.vertical, 2)
                        .background(RoundedRectangle(cornerRadius: 5).fill(Color.primary.opacity(0.08)))
                    Text("in a terminal and watch Pixie notice it.").font(.callout)
                }
                .accessibilityElement(children: .combine)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 12).fill(Self.pixieGreen.opacity(0.12)))
    }

    private func keycap(_ text: String) -> some View {
        Text(text)
            .font(.callout.weight(.medium))
            .padding(.horizontal, 7)
            .frame(minWidth: 26, minHeight: 24)
            .background(RoundedRectangle(cornerRadius: 6).fill(Color(nsColor: .controlBackgroundColor)))
            .overlay(RoundedRectangle(cornerRadius: 6).strokeBorder(.separator))
            .shadow(color: .black.opacity(0.12), radius: 0, y: 1)
    }

    // MARK: Parts

    private func pixie(_ state: String) -> some View {
        VStack(spacing: 0) {
            PixieSpriteView(state: state)
            Ellipse()
                .fill(Color.black.opacity(0.12))
                .frame(width: 84, height: 10)
                .offset(y: -6)
                .accessibilityHidden(true)
        }
    }

    private func tile(_ feature: OnboardingFeature, size: CGFloat) -> some View {
        let colors: [Color] = switch feature {
        case .voice: [Color(red: 0.13, green: 0.83, blue: 0.64), Color(red: 0, green: 0.65, blue: 0.50)]
        case .agents: [Color(red: 0.55, green: 0.48, blue: 1), Color(red: 0.37, green: 0.31, blue: 0.90)]
        case .sessions: [Color(red: 1, green: 0.68, blue: 0.29), Color(red: 0.94, green: 0.50, blue: 0.07)]
        case .canvas: [Color(red: 0.31, green: 0.70, blue: 1), Color(red: 0.10, green: 0.49, blue: 0.94)]
        }
        return RoundedRectangle(cornerRadius: size * 0.27)
            .fill(LinearGradient(colors: colors, startPoint: .top, endPoint: .bottom))
            .frame(width: size, height: size)
            .overlay(Image(systemName: feature.symbol)
                .font(.system(size: size * 0.5, weight: .semibold))
                .foregroundStyle(.white))
            .accessibilityHidden(true)
    }

    private func footer(_ page: OnboardingPage) -> some View {
        HStack {
            Button { state.flow.back() } label: {
                Label("Back", systemImage: "chevron.left")
            }
            .buttonStyle(.borderless)
            .foregroundStyle(.secondary)
            .frame(width: 170, alignment: .leading)
            Spacer()
            HStack(spacing: 6) {
                ForEach(Array(OnboardingFlow.pages.enumerated()), id: \.offset) { i, _ in
                    Capsule()
                        .fill(i <= state.flow.index ? Self.accent : Color.secondary.opacity(0.25))
                        .frame(width: 26, height: 5)
                }
            }
            .accessibilityElement()
            .accessibilityLabel("Step \(state.flow.index + 1) of \(OnboardingFlow.pages.count)")
            Spacer()
            Group {
                if page == .done {
                    Button(OnboardingFlow.finishTitle(restartPending: settings.restartText != nil)) { onFinish() }
                } else {
                    Button("Continue") { state.flow.next() }
                }
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .keyboardShortcut(.defaultAction)
            .frame(width: 170, alignment: .trailing)
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 12)
        .background(.bar)
        .overlay(alignment: .top) { Divider() }
    }

    private func card<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 0) { content() }
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(RoundedRectangle(cornerRadius: 12).fill(Color(nsColor: .controlBackgroundColor)))
            .clipShape(RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(.separator.opacity(0.6)))
            .shadow(color: .black.opacity(0.05), radius: 2, y: 1)
    }

    /// A Settings field by path, label left and the Settings control right.
    @ViewBuilder
    private func field(_ path: String, label: String) -> some View {
        if let spec = settings.spec(path: path) {
            HStack {
                Text(label).font(.callout)
                Spacer()
                SettingsControlView(model: settings, spec: spec)
                    .disabled(!settings.enabled)
            }
            if let error = spec.note.isError ? spec.note.text : nil {
                Text(error).font(.callout).foregroundStyle(.red)
            }
        }
    }

    /// `purpose`: a permission's use, added to its Allowed line.
    private func checkRow(_ check: OnboardingCheck, purpose: String? = nil) -> some View {
        HStack(alignment: .center, spacing: 10) {
            Image(systemName: check.ok ? "checkmark.circle.fill" : "exclamationmark.circle.fill")
                .foregroundStyle(check.ok ? Self.pixieGreen : Color.orange)
                .font(.body)
                .accessibilityLabel(check.ok ? "Ready" : "Needs attention")
            VStack(alignment: .leading, spacing: 1) {
                Text(check.title).font(.callout.weight(.semibold))
                Text(check.ok ? purpose.map { "Allowed. \($0)" } ?? check.detail : check.detail)
                    .font(.caption)
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
        case .allow(let p):
            Button(p == .microphone ? "Allow Microphone" : "Allow") { state.ask([p]) }
                .disabled(state.asking)
        case .openSettings(let p):
            Button("Open Settings") { state.openSettings(p) }
        case .installPix:
            Button("Install") {
                state.pix?.install()
                state.refreshPix()
            }
            .buttonStyle(.borderedProminent)
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
