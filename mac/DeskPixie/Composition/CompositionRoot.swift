import DeskPixieCore
import Foundation
import Observation

/// What the tray shows: the menu built from the core's `app.state`, plus problems only
/// Swift knows about (hotkey, audio, engine start), which lead the status lines.
@MainActor
@Observable
final class AppState {
    /// Last `app.state` from the core; nil until the first successful read.
    var core: TrayAppState?
    /// Launch progress: "Starting…" (and the permission hint) until the core is configured.
    var startup = StartupPhase.starting
    var pixieState = "idle"
    var hotkeyError: String?
    var engineError: String?
    var audioError: String?

    /// The Mic Mode picker seam (the tray row shows where it exists).
    let micModes: any MicModeSystem = AVMicModeSystem()

    var localStatusLines: [String] { [startup.statusLine, hotkeyError, engineError, audioError].compactMap { $0 } }

    var menuItems: [TrayItem] {
        TrayMenu.build(core, localStatusLines: localStatusLines, micModes: micModes, starting: startup.isStarting)
    }
}

/// Builds the object graph once, at launch. Everything is long-lived (app lifetime).
@MainActor
final class CompositionRoot {
    let appState = AppState()
    let engine = LibDeskPixieEngine()
    let voiceIO = VPIOVoiceIO()
    /// Playback ring: 120 s at 24 kHz, so a whole answer fits without back-pressure.
    let ring = PlaybackRing(capacity: VoiceAudioSizes.speakerRingSamples)
    /// Mic ring: 2 s at 24 kHz.
    let micRing = SampleRing(capacity: VoiceAudioSizes.micRingSamples)
    let pump: AudioPump
    let hotkey = CarbonHotkey()
    let coordinator: EngineCoordinator

    init() {
        // The gate empties the mic ring while the mic is off and pauses the stall clock while
        // no unit runs (units start on demand).
        pump = AudioPump(engine: engine, ring: ring, mic: micRing, gate: voiceIO.gate)
        coordinator = EngineCoordinator(appState: appState, engine: engine, voiceIO: voiceIO,
                                        ring: ring, micRing: micRing, pump: pump, hotkey: hotkey)
    }
}
