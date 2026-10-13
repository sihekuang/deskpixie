import CoreAudio
import Foundation

/// The real devices, read from the CoreAudio HAL on each call (ai-mascot `devices.rs` `mac`).
public struct CoreAudioDevices: AudioDeviceSource, Sendable {
    public init() {}

    public func devices() -> [AudioDevice] {
        Self.deviceIDs().compactMap(Self.device)
    }

    public func defaultDevice(_ dir: AudioDirection) -> UInt32? {
        let sel = dir == .input ? kAudioHardwarePropertyDefaultInputDevice : kAudioHardwarePropertyDefaultOutputDevice
        var addr = Self.address(sel)
        var id = AudioObjectID(kAudioObjectUnknown)
        var size = UInt32(MemoryLayout<AudioObjectID>.size)
        let st = AudioObjectGetPropertyData(AudioObjectID(kAudioObjectSystemObject), &addr, 0, nil, &size, &id)
        return st == noErr && id != kAudioObjectUnknown ? id : nil
    }

    /// Reads only UIDs: used when `alive` fails.
    public func find(uid: String) -> UInt32? {
        Self.deviceIDs().first { Self.string($0, kAudioDevicePropertyDeviceUID) == uid }
    }

    /// Reads only this device: the watcher calls it every second.
    public func alive(id: UInt32, uid: String) -> Bool {
        var addr = Self.address(kAudioDevicePropertyDeviceIsAlive)
        var alive: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        let st = AudioObjectGetPropertyData(id, &addr, 0, nil, &size, &alive)
        return st == noErr && alive != 0 && Self.string(id, kAudioDevicePropertyDeviceUID) == uid
    }

    // MARK: HAL reads

    static func address(_ selector: AudioObjectPropertySelector,
                        _ scope: AudioObjectPropertyScope = kAudioObjectPropertyScopeGlobal) -> AudioObjectPropertyAddress {
        AudioObjectPropertyAddress(mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
    }

    static func deviceIDs() -> [AudioObjectID] {
        var addr = address(kAudioHardwarePropertyDevices)
        let system = AudioObjectID(kAudioObjectSystemObject)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(system, &addr, 0, nil, &size) == noErr else { return [] }
        var ids = [AudioObjectID](repeating: 0, count: Int(size) / MemoryLayout<AudioObjectID>.size)
        guard !ids.isEmpty else { return [] }
        let st = ids.withUnsafeMutableBytes { buf in
            AudioObjectGetPropertyData(system, &addr, 0, nil, &size, buf.baseAddress!)
        }
        guard st == noErr else { return [] }
        return Array(ids.prefix(Int(size) / MemoryLayout<AudioObjectID>.size))
    }

    static func string(_ id: AudioObjectID, _ selector: AudioObjectPropertySelector) -> String? {
        var addr = address(selector)
        var value: Unmanaged<CFString>?
        var size = UInt32(MemoryLayout<Unmanaged<CFString>?>.size)
        let st = withUnsafeMutablePointer(to: &value) { ptr in
            AudioObjectGetPropertyData(id, &addr, 0, nil, &size, ptr)
        }
        guard st == noErr, let value else { return nil }
        return value.takeRetainedValue() as String  // a "copy" property: +1
    }

    static func transport(_ id: AudioObjectID) -> UInt32 {
        var addr = address(kAudioDevicePropertyTransportType)
        var t: UInt32 = 0
        var size = UInt32(MemoryLayout<UInt32>.size)
        return AudioObjectGetPropertyData(id, &addr, 0, nil, &size, &t) == noErr ? t : 0
    }

    /// Whether the device has any channels in `scope`.
    static func hasChannels(_ id: AudioObjectID, _ scope: AudioObjectPropertyScope) -> Bool {
        var addr = address(kAudioDevicePropertyStreamConfiguration, scope)
        var size: UInt32 = 0
        guard AudioObjectGetPropertyDataSize(id, &addr, 0, nil, &size) == noErr,
              Int(size) >= MemoryLayout<AudioBufferList>.size else { return false }
        let raw = UnsafeMutableRawPointer.allocate(byteCount: Int(size), alignment: MemoryLayout<AudioBufferList>.alignment)
        defer { raw.deallocate() }
        guard AudioObjectGetPropertyData(id, &addr, 0, nil, &size, raw) == noErr else { return false }
        let list = UnsafeMutableAudioBufferListPointer(raw.assumingMemoryBound(to: AudioBufferList.self))
        // Only the buffers that fit in the returned size are read.
        let header = MemoryLayout<AudioBufferList>.offset(of: \AudioBufferList.mBuffers) ?? 8
        let room = max(0, Int(size) - header) / MemoryLayout<AudioBuffer>.stride
        return list.prefix(min(list.count, room)).contains { $0.mNumberChannels > 0 }
    }

    static func device(_ id: AudioObjectID) -> AudioDevice? {
        guard let uid = string(id, kAudioDevicePropertyDeviceUID) else { return nil }
        let t = transport(id)
        return AudioDevice(
            id: id, uid: uid, name: string(id, kAudioObjectPropertyName) ?? uid,
            bluetooth: t == kAudioDeviceTransportTypeBluetooth || t == kAudioDeviceTransportTypeBluetoothLE,
            input: hasChannels(id, kAudioObjectPropertyScopeInput),
            output: hasChannels(id, kAudioObjectPropertyScopeOutput))
    }
}

/// Calls `handler` (on a private queue) whenever a device is plugged in or removed, so the
/// Settings device menus can be re-sent (`settings.choices`). Stops on `cancel()` or deinit.
public final class AudioDeviceListObserver: @unchecked Sendable {
    private let queue = DispatchQueue(label: "deskpixie.audio-device-list")
    private let lock = NSLock()
    private var block: AudioObjectPropertyListenerBlock?

    public init(_ handler: @escaping @Sendable () -> Void) {
        let block: AudioObjectPropertyListenerBlock = { _, _ in handler() }
        var addr = CoreAudioDevices.address(kAudioHardwarePropertyDevices)
        if AudioObjectAddPropertyListenerBlock(AudioObjectID(kAudioObjectSystemObject), &addr, queue, block) == noErr {
            self.block = block
        }
    }

    deinit { cancel() }

    public func cancel() {
        guard let b = lock.withLock({ () -> AudioObjectPropertyListenerBlock? in
            defer { block = nil }
            return block
        }) else { return }
        var addr = CoreAudioDevices.address(kAudioHardwarePropertyDevices)
        _ = AudioObjectRemovePropertyListenerBlock(AudioObjectID(kAudioObjectSystemObject), &addr, queue, b)
    }
}
