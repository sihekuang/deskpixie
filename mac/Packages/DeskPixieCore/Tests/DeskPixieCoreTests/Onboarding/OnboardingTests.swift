import XCTest
@testable import DeskPixieCore

final class OnboardingTests: XCTestCase {
    private func values(_ json: String) -> JSONValue {
        try! JSONDecoder().decode(JSONValue.self, from: Data(json.utf8))
    }

    // MARK: Gate

    func testOpensAtLaunchOnlyForANewUserWhoHasNotSeenIt() {
        XCTAssertTrue(OnboardingGate.shouldOpenAtLaunch(done: false, settingsExisted: false, pending: false))
        XCTAssertFalse(OnboardingGate.shouldOpenAtLaunch(done: true, settingsExisted: false, pending: false))
        // Someone upgrading already has settings: never opened for them on its own.
        XCTAssertFalse(OnboardingGate.shouldOpenAtLaunch(done: false, settingsExisted: true, pending: false))
    }

    /// The core writes settings.json on the first launch: a new user whose first launch never got
    /// to the window (a crash, a quit while starting) still gets it next time.
    func testANewUserWhoNeverSawItStillGetsItOnALaterLaunch() {
        XCTAssertTrue(OnboardingGate.shouldOpenAtLaunch(done: false, settingsExisted: true, pending: true))
        XCTAssertFalse(OnboardingGate.shouldOpenAtLaunch(done: true, settingsExisted: true, pending: true))
    }

    /// Pending belongs to one data folder: an isolated test copy's first run never makes the real
    /// app open it.
    func testPendingCountsOnlyForTheDataFolderItWasSetFor() {
        let real = URL(fileURLWithPath: "/Users/x/Library/Application Support/com.sihekuang.deskpixie/settings.json")
        XCTAssertTrue(OnboardingGate.isPending(stored: real.path, settingsFile: real))
        XCTAssertFalse(OnboardingGate.isPending(stored: "/tmp/dpx-e2e/settings.json", settingsFile: real))
        XCTAssertFalse(OnboardingGate.isPending(stored: nil, settingsFile: real))
    }

    func testTheDockIconStaysWhileAnotherAppWindowIsOpen() {
        let settings = DockPresence.Window(id: "settings", visible: true, miniaturized: false)
        let onboarding = DockPresence.Window(id: "onboarding", visible: true, miniaturized: false)
        let canvas = DockPresence.Window(id: "canvas", visible: true, miniaturized: false)
        // Closing Settings while onboarding is open keeps the Dock icon, and the other way round.
        XCTAssertTrue(DockPresence.keepsDockIcon(closing: "settings", windows: [settings, onboarding]))
        XCTAssertTrue(DockPresence.keepsDockIcon(closing: "onboarding", windows: [settings, onboarding]))
        // A minimized one still needs its Dock tile.
        let minimized = DockPresence.Window(id: "settings", visible: false, miniaturized: true)
        XCTAssertTrue(DockPresence.keepsDockIcon(closing: "onboarding", windows: [minimized, onboarding]))
        // Only the closing one, or windows that never show in the Dock: back to the menu bar.
        XCTAssertFalse(DockPresence.keepsDockIcon(closing: "onboarding", windows: [onboarding, canvas]))
        XCTAssertFalse(DockPresence.keepsDockIcon(closing: "settings", windows: [settings]))
    }

    func testTheSettingsFileIsTheOneInTheAppDataFolder() {
        let dir = URL(fileURLWithPath: "/tmp/dpx")
        XCTAssertEqual(OnboardingGate.settingsFile(appSupport: dir).path, "/tmp/dpx/settings.json")
    }

    // MARK: Pages

    func testTheChosenFeaturesFollowTheirSettings() {
        XCTAssertEqual(OnboardingFlow.chosen(SettingsFixture.defaults), [.voice, .agents, .canvas])
        // No snapshot yet: the core's defaults.
        XCTAssertEqual(OnboardingFlow.chosen(.null), [.voice, .agents, .canvas])
        let v = values(#"{"agent":{"enabled":false},"notifications":{"sessionTurns":true},"canvas":{"autoOpen":false}}"#)
        XCTAssertEqual(OnboardingFlow.chosen(v), [.voice, .sessions])
    }

    func testEachChosenFeatureWithSetupGetsAPageBeforeDone() {
        XCTAssertEqual(OnboardingFlow.pages(chosen: [.voice, .agents, .sessions, .canvas]),
                       [.welcome, .choose, .setup(.voice), .setup(.agents), .setup(.sessions), .done])
        XCTAssertEqual(OnboardingFlow.pages(chosen: [.voice, .canvas]), [.welcome, .choose, .setup(.voice), .done])
    }

    func testOnlyTheOptionalFeaturesHaveASwitch() {
        XCTAssertNil(OnboardingFeature.voice.settingPath)
        XCTAssertEqual(OnboardingFeature.agents.settingPath, "agent.enabled")
        XCTAssertEqual(OnboardingFeature.sessions.settingPath, "notifications.sessionTurns")
        XCTAssertEqual(OnboardingFeature.canvas.settingPath, "canvas.autoOpen")
    }

    func testNextAndBackStayWithinThePages() {
        let pages = OnboardingFlow.pages(chosen: [.voice])
        var flow = OnboardingFlow()
        XCTAssertEqual(flow.page(in: pages), .welcome)
        XCTAssertFalse(flow.canGoBack)
        flow.back(in: pages)
        XCTAssertEqual(flow.page(in: pages), .welcome)
        flow.next(in: pages)
        flow.next(in: pages)
        flow.next(in: pages)
        XCTAssertEqual(flow.page(in: pages), .done)
        XCTAssertTrue(flow.isLast(in: pages))
        flow.next(in: pages)
        XCTAssertEqual(flow.page(in: pages), .done)
        flow.back(in: pages)
        XCTAssertEqual(flow.page(in: pages), .setup(.voice))
    }

    /// Turning a feature off on a later visit can shorten the pages under the current index.
    func testAPageIndexPastTheEndShowsDone() {
        var flow = OnboardingFlow()
        let long = OnboardingFlow.pages(chosen: [.voice, .agents, .sessions])
        for _ in 0..<5 { flow.next(in: long) }
        XCTAssertEqual(flow.page(in: long), .done)
        let short = OnboardingFlow.pages(chosen: [.voice])
        XCTAssertEqual(flow.page(in: short), .done)
        flow.back(in: short)
        XCTAssertEqual(flow.page(in: short), .setup(.voice))
    }

    func testTheFinishButtonSaysWhenItRestarts() {
        XCTAssertEqual(OnboardingFlow.finishTitle(restartPending: false), "Finish")
        XCTAssertEqual(OnboardingFlow.finishTitle(restartPending: true), "Finish and Restart")
    }

    // MARK: Voice checks

    func testTheKeyCheckIsTheVoiceServicesKey() {
        XCTAssertEqual(OnboardingChecks.secretID(forProvider: "openai-realtime"), .openAI)
        XCTAssertEqual(OnboardingChecks.secretID(forProvider: "openai-live"), .openAI)
        XCTAssertEqual(OnboardingChecks.secretID(forProvider: "gemini-live"), .gemini)
        XCTAssertEqual(OnboardingChecks.secretID(forProvider: nil), .openAI)

        let found = OnboardingChecks.voiceKey(provider: "gemini-live",
                                              secrets: SettingsSecrets(openai: .missing, gemini: .found,
                                                                       sources: SettingsKeySources(openai: "", gemini: "keychain")))
        XCTAssertTrue(found.ok)
        XCTAssertEqual(found.detail, "From Keychain")

        let missing = OnboardingChecks.voiceKey(provider: "gemini-live", secrets: SettingsSecrets(openai: .found, gemini: .missing))
        XCTAssertFalse(missing.ok)
        XCTAssertEqual(missing.title, "GEMINI_API_KEY")
        XCTAssertTrue(missing.detail.contains("Paste"))

        XCTAssertFalse(OnboardingChecks.voiceKey(provider: nil, secrets: nil).ok)
    }

    func testTheMicrophoneCheckOffersTheFixThatWorksNow() {
        let granted = OnboardingChecks.microphone(.granted)
        XCTAssertTrue(granted.ok)
        XCTAssertNil(granted.fix)
        XCTAssertEqual(OnboardingChecks.microphone(.notDetermined).fix, .allowMicrophone)
        let denied = OnboardingChecks.microphone(.denied)
        XCTAssertFalse(denied.ok)
        XCTAssertEqual(denied.fix, .openMicrophoneSettings)
    }

    // MARK: Agent checks

    func testTheAgentChecksAreTheSessionHostAndTheCoordinatorsProgram() {
        let programs = SetupPrograms(herdr: "/opt/homebrew/bin/herdr", tmux: "", claude: "", codex: "/usr/local/bin/codex")
        let claude = OnboardingChecks.agents(values: SettingsFixture.defaults, programs: programs, home: "/Users/x")
        XCTAssertEqual(claude.map(\.title), ["herdr", "Claude Code"])
        XCTAssertEqual(claude.map(\.ok), [true, false])
        XCTAssertEqual(claude[0].detail, "Found at /opt/homebrew/bin/herdr")
        guard case .link(let label, let url)? = claude[1].fix else { return XCTFail("no link: \(claude[1])") }
        XCTAssertEqual(label, "Get Claude Code")
        XCTAssertEqual(url.host, "docs.anthropic.com")

        let codex = OnboardingChecks.agents(values: values(#"{"agent":{"sessionHost":"tmux","coordinatorKind":"codex"}}"#),
                                            programs: programs, home: "/Users/x")
        XCTAssertEqual(codex.map(\.title), ["tmux", "Codex"])
        XCTAssertEqual(codex.map(\.ok), [false, true])
        guard case .link(let tmuxLabel, _)? = codex[0].fix else { return XCTFail("no link: \(codex[0])") }
        XCTAssertEqual(tmuxLabel, "Get tmux")
    }

    func testAProgramUnderHomeIsShownWithATilde() {
        let programs = SetupPrograms(herdr: "/Users/x/.local/bin/herdr", tmux: "", claude: "/Users/x/.local/bin/claude", codex: "")
        let checks = OnboardingChecks.agents(values: SettingsFixture.defaults, programs: programs, home: "/Users/x")
        XCTAssertEqual(checks.map(\.detail), ["Found at ~/.local/bin/herdr", "Found at ~/.local/bin/claude"])
    }

    func testUncheckedProgramsAreNotReportedFound() {
        let checks = OnboardingChecks.agents(values: SettingsFixture.defaults, programs: nil, home: "/Users/x")
        XCTAssertEqual(checks.map(\.ok), [false, false])
        XCTAssertTrue(checks.allSatisfy { $0.fix == nil })
        XCTAssertEqual(checks.map(\.detail), ["Checking…", "Checking…"])
    }

    func testAFailedCheckSaysSoInsteadOfCheckingForever() {
        let checks = OnboardingChecks.agents(values: SettingsFixture.defaults, programs: nil, home: "/Users/x", checkFailed: true)
        XCTAssertEqual(checks.map(\.ok), [false, false])
        XCTAssertTrue(checks.allSatisfy { $0.detail.hasPrefix("Couldn't check") })
    }

    func testTheSessionHostIsNamedForTheAgentsPage() {
        XCTAssertEqual(OnboardingChecks.sessionHostName(SettingsFixture.defaults), "herdr")
        XCTAssertEqual(OnboardingChecks.sessionHostName(values(#"{"agent":{"sessionHost":"tmux"}}"#)), "tmux")
    }

    func testOnlyTheCoordinatorsTrayLinesShowOnTheAgentsPage() {
        let lines = ["Hotkey unavailable: ctrl+alt+Space", "Coordinator: herdr version mismatch (update herdr)", "Voice: no API key"]
        XCTAssertEqual(OnboardingChecks.coordinatorLines(lines), ["Coordinator: herdr version mismatch (update herdr)"])
    }

    // MARK: pix

    func testPixIsReadyOnlyWhenInstalledAndOnThePath() {
        XCTAssertTrue(OnboardingChecks.pix(state: .installed, onPath: true).ok)
        let offPath = OnboardingChecks.pix(state: .installed, onPath: false)
        XCTAssertFalse(offPath.ok)
        XCTAssertNil(offPath.fix)
        XCTAssertEqual(OnboardingChecks.pix(state: .notInstalled, onPath: true).fix, .installPix)
        XCTAssertEqual(OnboardingChecks.pix(state: .broken, onPath: true).fix, .installPix)
        XCTAssertNil(OnboardingChecks.pix(state: .other("a file"), onPath: true).fix)
    }

    // MARK: Done

    func testTheSummaryHasALinePerChosenFeature() {
        let ok = OnboardingCheck(title: "Microphone", ok: true, detail: "Allowed")
        let bad = OnboardingCheck(title: "Claude Code", ok: false, detail: "Not found")
        let lines = OnboardingChecks.summary(chosen: [.voice, .agents, .canvas],
                                             checks: [.voice: [ok], .agents: [ok, bad]])
        XCTAssertEqual(lines.map(\.feature), [.voice, .agents, .canvas])
        XCTAssertEqual(lines.map(\.ok), [true, false, true])
        XCTAssertEqual(lines[0].detail, OnboardingFeature.voice.readyText)
        XCTAssertEqual(lines[1].detail, "Claude Code: Not found")
    }
}
