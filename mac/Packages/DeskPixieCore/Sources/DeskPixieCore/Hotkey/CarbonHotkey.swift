import Carbon
import Foundation
import os

/// Global key+modifier hotkey via Carbon `RegisterEventHotKey` (no TCC permission needed).
/// Port of Howl's `CarbonHotkeyMonitor` key+modifier path, press only.
/// Default: ⌃⌥Space (ai-mascot's `ctrl+alt+Space`).
@MainActor
public final class CarbonHotkey {
    public enum Failure: Error, Equatable {
        case installHandler(OSStatus)
        /// -9878 (`eventHotKeyExistsErr`) when another app holds the binding.
        case register(OSStatus)
    }

    public static let defaultKeyCode = UInt32(kVK_Space)
    public static let defaultModifiers = UInt32(controlKey | optionKey)

    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "hotkey")
    private static let signature: OSType = 0x4450_4958 // "DPIX"

    private var hotKeyRef: EventHotKeyRef?
    private var eventHandler: EventHandlerRef?
    private var onPress: (@MainActor () -> Void)?

    public init() {}

    /// Registers the hotkey; `onPress` runs on the main actor for each press.
    public func start(keyCode: UInt32 = defaultKeyCode, modifiers: UInt32 = defaultModifiers,
                      onPress: @escaping @MainActor () -> Void) throws {
        stop()
        self.onPress = onPress

        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let handler: EventHandlerUPP = { _, _, userData -> OSStatus in
            guard let userData else { return OSStatus(eventNotHandledErr) }
            let bits = UInt(bitPattern: userData)
            // Carbon delivers hotkey events on the main thread's event loop.
            DispatchQueue.main.async {
                MainActor.assumeIsolated {
                    let me = Unmanaged<CarbonHotkey>.fromOpaque(UnsafeRawPointer(bitPattern: bits)!).takeUnretainedValue()
                    me.fire()
                }
            }
            return noErr
        }
        var installed: EventHandlerRef?
        let rc = InstallEventHandler(GetApplicationEventTarget(), handler, 1, &spec,
                                     Unmanaged.passUnretained(self).toOpaque(), &installed)
        guard rc == noErr else {
            Self.log.error("InstallEventHandler failed rc=\(rc, privacy: .public)")
            throw Failure.installHandler(rc)
        }
        eventHandler = installed

        var ref: EventHotKeyRef?
        let reg = RegisterEventHotKey(keyCode, modifiers, EventHotKeyID(signature: Self.signature, id: 1),
                                      GetApplicationEventTarget(), 0, &ref)
        guard reg == noErr else {
            Self.log.error("RegisterEventHotKey failed rc=\(reg, privacy: .public) (binding taken by another app?)")
            stop()
            throw Failure.register(reg)
        }
        hotKeyRef = ref
        Self.log.notice("hotkey registered key=\(keyCode, privacy: .public) mods=\(String(format: "0x%X", modifiers), privacy: .public)")
    }

    public func stop() {
        if let ref = hotKeyRef { UnregisterEventHotKey(ref) }
        if let h = eventHandler { RemoveEventHandler(h) }
        hotKeyRef = nil
        eventHandler = nil
        onPress = nil
    }

    private func fire() {
        Self.log.info("hotkey pressed")
        onPress?()
    }
}
