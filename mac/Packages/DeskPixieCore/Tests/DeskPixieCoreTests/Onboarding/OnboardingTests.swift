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

    /// Five steps whatever is chosen: choosing and setting up share one page.
    func testThereAreFiveSteps() {
        XCTAssertEqual(OnboardingFlow.pages, [.welcome, .pixie, .setup, .permissions, .done])
    }

    func testTheWindowCanStartOnAnyStep() {
        var flow = OnboardingFlow()
        flow.restart(at: .permissions)
        XCTAssertEqual(flow.page, .permissions)
        flow.next()
        XCTAssertEqual(flow.page, .done)
        flow.restart()
        XCTAssertEqual(flow.page, .welcome)
    }

    // MARK: Permissions at launch (#213)

    /// An onboarded user sees the Permissions step again only for something not asked yet, once
    /// per version.
    func testThePermissionsStepReopensOncePerVersionForSomethingUnasked() {
        XCTAssertTrue(OnboardingGate.shouldOpenPermissions(done: true, version: "0.8.12 (8120)", checkedVersion: "0.8.11 (811)",
                                                           unasked: [.notifications]))
        XCTAssertTrue(OnboardingGate.shouldOpenPermissions(done: true, version: "0.8.12 (8120)", checkedVersion: nil,
                                                           unasked: [.accessibility]))
        XCTAssertFalse(OnboardingGate.shouldOpenPermissions(done: true, version: "0.8.12 (8120)", checkedVersion: "0.8.12 (8120)",
                                                            unasked: [.notifications]), "already shown for this version")
        XCTAssertFalse(OnboardingGate.shouldOpenPermissions(done: true, version: "0.8.12 (8120)", checkedVersion: nil,
                                                            unasked: []), "nothing to ask")
        XCTAssertFalse(OnboardingGate.shouldOpenPermissions(done: false, version: "0.8.12 (8120)", checkedVersion: nil,
                                                            unasked: [.microphone]), "a new user gets the whole window")
    }

    /// Only what can be read counts: Automation for apps that are running.
    func testTheLaunchCheckReadsOnlyWhatCanBeRead() {
        XCTAssertEqual(AppPermission.launchCheck(running: []), [.microphone, .notifications, .accessibility])
        XCTAssertEqual(AppPermission.launchCheck(running: [.music, .systemEvents]),
                       [.microphone, .notifications, .accessibility, .automation(.systemEvents), .automation(.music)])
    }

    /// A pack's preview comes from the folder the core loads it from: the user's beats bundled.
    func testAPacksPreviewIsTheFolderTheCoreLoads() {
        let bundled = URL(fileURLWithPath: "/App/Resources/pixies")
        let user = URL(fileURLWithPath: "/Support/pixies")
        let have: Set<String> = ["/App/Resources/pixies/bit/pixie.json", "/App/Resources/pixies/dino/pixie.json",
                                 "/Support/pixies/dino/pixie.json"]
        let exists = { (u: URL) in have.contains(u.path) }
        XCTAssertEqual(OnboardingPixies.folder(id: "bit", roots: [bundled, user], exists: exists)?.path, "/App/Resources/pixies/bit")
        XCTAssertEqual(OnboardingPixies.folder(id: "dino", roots: [bundled, user], exists: exists)?.path, "/Support/pixies/dino")
        XCTAssertNil(OnboardingPixies.folder(id: "nope", roots: [bundled, user], exists: exists))
        XCTAssertNil(OnboardingPixies.folder(id: "../bit", roots: [bundled, user], exists: { _ in true }))
    }

    /// On the setup page a feature's setup shows under its switch while it is on; canvas has none.
    func testAFeatureShowsItsSetupWhileItIsOn() {
        XCTAssertEqual(OnboardingFlow.setupShown(chosen: [.voice, .agents, .sessions, .canvas]), [.voice, .agents, .sessions])
        XCTAssertEqual(OnboardingFlow.setupShown(chosen: [.voice, .canvas]), [.voice])
    }

    func testOnlyTheOptionalFeaturesHaveASwitch() {
        XCTAssertNil(OnboardingFeature.voice.settingPath)
        XCTAssertEqual(OnboardingFeature.agents.settingPath, "agent.enabled")
        XCTAssertEqual(OnboardingFeature.sessions.settingPath, "notifications.sessionTurns")
        XCTAssertEqual(OnboardingFeature.canvas.settingPath, "canvas.autoOpen")
    }

    func testNextAndBackStayWithinTheSteps() {
        var flow = OnboardingFlow()
        XCTAssertEqual(flow.page, .welcome)
        XCTAssertFalse(flow.canGoBack)
        flow.back()
        XCTAssertEqual(flow.page, .welcome)
        flow.next()
        XCTAssertEqual(flow.page, .pixie)
        flow.next()
        XCTAssertEqual(flow.page, .setup)
        flow.next()
        XCTAssertEqual(flow.page, .permissions)
        flow.next()
        XCTAssertEqual(flow.page, .done)
        XCTAssertTrue(flow.isLast)
        flow.next()
        XCTAssertEqual(flow.page, .done)
        flow.back()
        XCTAssertEqual(flow.page, .permissions)
        flow.restart()
        XCTAssertEqual(flow.page, .welcome)
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
        XCTAssertEqual(OnboardingChecks.microphone(.notDetermined).fix, .allow(.microphone))
        let denied = OnboardingChecks.microphone(.denied)
        XCTAssertFalse(denied.ok)
        XCTAssertEqual(denied.fix, .openSettings(.microphone))
    }

    // MARK: Projects folder (#216)

    func testTheProjectsFolderCheckOffersCreateOnlyWhenItIsMissing() {
        let home = "/Users/u"
        let path = "/Users/u/Documents/deskpixie"
        let ready = OnboardingChecks.projectsFolder(ProjectsFolderStatus(state: .ready, path: path, fallback: "/d", detail: "Found"), home: home)
        XCTAssertTrue(ready.ok)
        XCTAssertEqual(ready.detail, "~/Documents/deskpixie")
        XCTAssertNil(ready.fix)
        let missing = OnboardingChecks.projectsFolder(ProjectsFolderStatus(state: .missing, path: path, fallback: "/d",
                                                                           detail: "Doesn't exist yet."), home: home)
        XCTAssertFalse(missing.ok)
        XCTAssertEqual(missing.fix, .createProjectsFolder)
        XCTAssertEqual(missing.detail, "~/Documents/deskpixie: Doesn't exist yet.")
        let unusable = OnboardingChecks.projectsFolder(ProjectsFolderStatus(state: .unusable, path: "", fallback: "/d",
                                                                            detail: "No folder is set."), home: home)
        XCTAssertFalse(unusable.ok)
        XCTAssertNil(unusable.fix)
        XCTAssertEqual(unusable.detail, "No folder is set.")
        XCTAssertEqual(OnboardingChecks.projectsFolder(nil, home: home).detail, "Checking…")
    }

    // MARK: Permissions (#210)

    func testEveryPermissionIsListedWithAutomationOnlyForInstalledApps() {
        XCTAssertEqual(AppPermission.all(installed: []),
                       [.microphone, .notifications, .accessibility, .automation(.systemEvents)])
        XCTAssertEqual(AppPermission.all(installed: [.spotify, .iTerm]),
                       [.microphone, .notifications, .accessibility, .automation(.systemEvents),
                        .automation(.iTerm), .automation(.spotify)])
        XCTAssertEqual(AppPermission.automation(.music).title, "Control Music")
    }

    /// Allow All asks only what's unanswered, and Accessibility (System Settings) last.
    func testAllowAllAsksWhatIsUnansweredWithAccessibilityLast() {
        let asked = AppPermission.toAsk([(.microphone, .granted), (.notifications, .notDetermined),
                                         (.accessibility, .notDetermined), (.automation(.systemEvents), .notDetermined),
                                         (.automation(.music), .denied)], askedAccessibility: false)
        XCTAssertEqual(asked, [.notifications, .automation(.systemEvents), .accessibility])
        XCTAssertEqual(AppPermission.toAsk([(.microphone, .granted)], askedAccessibility: false), [])
        // Asked once, Accessibility is System Settings' to turn on: Allow All doesn't send you back.
        XCTAssertEqual(AppPermission.toAsk([(.accessibility, .notDetermined)], askedAccessibility: true), [])
    }

    /// An app quit after its question can't be read: the answer it gave stands.
    func testAnAnswerStandsWhenTheAppCanNoLongerBeRead() {
        XCTAssertEqual(AppPermission.merged(read: .notDetermined, answered: .granted), .granted)
        XCTAssertEqual(AppPermission.merged(read: .notDetermined, answered: nil), .notDetermined)
        XCTAssertEqual(AppPermission.merged(read: .denied, answered: .granted), .denied)
    }

    func testAPermissionLineOffersTheFixThatWorksNow() {
        XCTAssertTrue(OnboardingChecks.permission(.notifications, .granted, asked: false).ok)
        let open = OnboardingChecks.permission(.automation(.iTerm), .notDetermined, asked: false)
        XCTAssertEqual(open.fix, .allow(.automation(.iTerm)))
        XCTAssertTrue(open.detail.contains("iTerm"), open.detail)
        let denied = OnboardingChecks.permission(.notifications, .denied, asked: true)
        XCTAssertEqual(denied.fix, .openSettings(.notifications))
        XCTAssertTrue(denied.detail.contains("Notifications"), denied.detail)
        // Accessibility can't say no: once asked, the way on is System Settings.
        XCTAssertEqual(OnboardingChecks.permission(.accessibility, .notDetermined, asked: false).fix, .allow(.accessibility))
        XCTAssertEqual(OnboardingChecks.permission(.accessibility, .notDetermined, asked: true).fix, .openSettings(.accessibility))
    }

    func testThePermissionsDoneLineCountsWhatIsLeft() {
        let ok = OnboardingCheck(title: "a", ok: true, detail: "")
        let no = OnboardingCheck(title: "b", ok: false, detail: "")
        XCTAssertEqual(OnboardingChecks.permissionsSummary([ok, ok]).detail, "All allowed")
        XCTAssertEqual(OnboardingChecks.permissionsSummary([ok, no]).detail, "1 still to allow")
        XCTAssertEqual(OnboardingChecks.permissionsSummary([no, no]).detail, "2 still to allow")
        XCTAssertFalse(OnboardingChecks.permissionsSummary([no]).ok)
    }

    func testEachPermissionOpensItsOwnSettingsPane() {
        XCTAssertTrue(AppPermission.accessibility.settingsURL.absoluteString.hasSuffix("Privacy_Accessibility"))
        XCTAssertTrue(AppPermission.automation(.spotify).settingsURL.absoluteString.hasSuffix("Privacy_Automation"))
        XCTAssertTrue(AppPermission.notifications.settingsURL.absoluteString.hasSuffix("preference.notifications"))
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

    /// A feature card's header says at a glance whether its setup is done; canvas has nothing to check.
    func testACardsStatusCountsWhatIsStillToDo() {
        let ok = OnboardingCheck(title: "Microphone", ok: true, detail: "Allowed")
        let bad = OnboardingCheck(title: "Key", ok: false, detail: "Missing")
        XCTAssertEqual(OnboardingChecks.status([ok, ok]), "Ready")
        XCTAssertEqual(OnboardingChecks.status([bad, ok]), "1 to do")
        XCTAssertEqual(OnboardingChecks.status([bad, bad]), "2 to do")
        XCTAssertNil(OnboardingChecks.status([]))
    }

    /// Done's "Try it" line shows the voice hotkey as keycaps; no hotkey set: the core's default.
    func testTheHotkeyShowsAsKeycaps() {
        XCTAssertEqual(OnboardingChecks.hotkeyCaps("ctrl+alt+Space"), ["⌃", "⌥", "Space"])
        XCTAssertEqual(OnboardingChecks.hotkeyCaps("alt+super+KeyK"), ["⌥", "⌘", "K"])
        XCTAssertEqual(OnboardingChecks.hotkeyCaps(nil), ["⌃", "⌥", "Space"])
        XCTAssertEqual(OnboardingChecks.hotkeyCaps(""), ["⌃", "⌥", "Space"])
    }

    func testEachFeatureHasASymbol() {
        XCTAssertEqual(OnboardingFeature.allCases.map(\.symbol), ["waveform", "sparkles", "terminal", "square.on.square"])
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
