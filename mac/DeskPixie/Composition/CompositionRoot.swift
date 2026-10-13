import DeskPixieCore
import Foundation
import Observation

/// What the tray shows: the status line and the last mascot state.
@MainActor
@Observable
final class AppState {
    var status = "Starting…"
    var mascotState = "idle"
}

/// Builds the object graph once, at launch. Everything is long-lived (app lifetime).
@MainActor
final class CompositionRoot {
    let appState = AppState()
    let engine = LibDeskPixieEngine()
    let voiceIO = VPIOVoiceIO()
    /// Playback ring: 120 s at 24 kHz, so a whole answer fits without back-pressure.
    let ring = PlaybackRing(capacity: 24_000 * 120)
    /// Mic ring: 2 s at 24 kHz.
    let micRing = SampleRing(capacity: 48_000)
    let pump: AudioPump
    let hotkey = CarbonHotkey()
    let coordinator: EngineCoordinator

    init() {
        pump = AudioPump(engine: engine, ring: ring, mic: micRing)
        coordinator = EngineCoordinator(appState: appState, engine: engine, voiceIO: voiceIO,
                                        ring: ring, micRing: micRing, pump: pump, hotkey: hotkey)
    }
}
