import AudioToolbox
import CDP
import CoreAudio
import Darwin
import Foundation
import os

/// One `kAudioUnitSubType_VoiceProcessingIO` unit (echo cancellation) for both the mic
/// (input element 1) and the speaker (output element 0), 24 kHz mono Int16 on both sides.
/// Port of ai-mascot `crates/audio-macos/src/engine.rs`.
///
/// - `start` only arms it: the unit is built on demand (audio queued in the playback ring,
///   or the mic switched on) and stopped after 300 ms with the mic off and the speaker idle;
///   a failed build backs off 1 s (`VoiceUnitControl`, polled every 20 ms on a private queue).
/// - Devices: the UIDs from `setPinned` are used when connected, else the Mac's default
///   (`AudioDevices.pick`); a unit that can't start on them is retried on the defaults.
/// - Device watch: polled every 1 s while a unit runs (3 s settle, 2 polls, a pinned device
///   must keep its AudioDeviceID). A real change drops the unit, discards queued playback,
///   mutes the mic and sends `.failed(.deviceChanged)` to the current conversation; the app
///   forwards it to `audio.deviceChanged`. (Phase 3 rebuilt the unit from a default-device
///   property listener instead; with on-demand units a new conversation always starts on
///   the current devices, and a change mid-conversation ends it, as in ai-mascot.)
/// - Permission (`MicSwitch`): `setMicEnabled(true)` asks when the user was never asked
///   (the mic stays off; the Go runtime waits up to 60 s for the first frame); a denial
///   sends `.failed(.permissionDenied)`.
public final class VPIOVoiceIO: VoiceIO, @unchecked Sendable {
    static let tickInterval: DispatchTimeInterval = .milliseconds(20)
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "audio")
    private static let queueKey = DispatchSpecificKey<Bool>()

    /// Mic on / unit running flags. Pass it to `AudioPump(…, gate:)`.
    public let gate = AudioGate()
    private let micState: MicState
    private let mic: MicSwitch
    private let auth: MicAuthorizer
    private let devices: AudioDeviceSource & Sendable
    private let queue = DispatchQueue(label: "deskpixie.audio-control", qos: .userInitiated)

    // Under `lock`.
    private let lock = NSLock()
    private var pinned: (input: String?, output: String?) = (nil, nil)
    private var handler: (@Sendable (MicEvent) -> Void)?

    // On `queue` only.
    private var control: VoiceUnitControl?
    private var timer: DispatchSourceTimer?

    public init(auth: MicAuthorizer = AVMicAuthorizer(), devices: AudioDeviceSource & Sendable = CoreAudioDevices()) {
        self.auth = auth
        self.devices = devices
        let state = MicState(gate: gate)
        micState = state
        let queue = queue, owner = WeakBox<VPIOVoiceIO>()
        mic = MicSwitch(state: state, auth: auth, onStarted: {
            queue.async { owner.value?.control?.micStarted(now: Self.now()) }
        })
        owner.value = self
        queue.setSpecific(key: Self.queueKey, value: true)
    }

    deinit {
        stop()
    }

    // MARK: VoiceIO

    public func start(playback: PlaybackRing,
                      onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) async throws {
        // Asking happens when a conversation turns the mic on; only a past denial fails here.
        if auth.status() == .denied { throw VoiceIOError.permissionDenied }
        onQueue { arm(playback: playback, onMicSamples: onMicSamples) }
    }

    public func setMicEnabled(_ on: Bool) {
        guard on else {
            mic.stop()
            return
        }
        do {
            let started = try mic.start { [weak self] event in self?.deliver(event) }
            Self.log.notice("mic on: \(started == .live ? "live" : "awaiting permission", privacy: .public)")
        } catch {
            Self.log.error("mic on: permission denied")
            deliver(.failed(.permissionDenied))
        }
    }

    public func setPinned(input: String?, output: String?) {
        lock.withLock { pinned = (input, output) }
    }

    public func setEventHandler(_ handler: @escaping @Sendable (MicEvent) -> Void) {
        lock.withLock { self.handler = handler }
    }

    public func stop() {
        mic.stop()
        onQueue {
            timer?.cancel()
            timer = nil
            control?.shutdown()
            control = nil
        }
    }

    // MARK: Control

    private static func now() -> TimeInterval { ProcessInfo.processInfo.systemUptime }

    /// Runs `body` on the control queue and waits for it (inline when already there).
    private func onQueue(_ body: () -> Void) {
        if DispatchQueue.getSpecific(key: Self.queueKey) == true { body() } else { queue.sync(execute: body) }
    }

    private func deliver(_ event: MicEvent) {
        lock.withLock { handler }?(event)
    }

    /// On the queue.
    private func arm(playback: PlaybackRing,
                     onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) {
        guard control == nil else { return }
        let gate = gate, micState = micState, devices = devices
        control = VoiceUnitControl(
            source: { devices },
            build: { chosen in try VPIOUnit.build(chosen, playback: playback, gate: gate, onMic: onMicSamples) },
            hooks: VoiceUnitControl.Hooks(
                saved: { [weak self] in self?.lock.withLock { self?.pinned } ?? (nil, nil) },
                micOn: { gate.micOn },
                speakerIdle: { playback.isIdle },
                discardSpeaker: {
                    let n = playback.discardAll()
                    if n > 0 { Self.log.notice("discarded \(n, privacy: .public) samples queued for no unit") }
                },
                mute: { micState.mute() },
                report: { micState.send(.failed($0)) },
                log: { Self.log.notice("\($0, privacy: .public)") },
                runningChanged: { gate.unitRunning = $0 }))
        let t = DispatchSource.makeTimerSource(queue: queue)
        t.schedule(deadline: .now(), repeating: Self.tickInterval, leeway: .milliseconds(5))
        t.setEventHandler { [weak self] in self?.control?.tick(now: Self.now()) }
        timer = t
        t.resume()
    }
}

/// Lets a closure created in `init` reach `self` weakly once `init` finishes.
private final class WeakBox<T: AnyObject>: @unchecked Sendable {
    weak var value: T?
}

/// One built and started VoiceProcessingIO unit.
final class VPIOUnit: VoiceUnitHandle, AudioDeviceTarget {
    private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "audio")

    let unit: AudioUnit
    private var context: VPIOContext?

    private init(unit: AudioUnit) {
        self.unit = unit
    }

    /// Builds and starts a unit on `chosen`; returns it and the devices it really uses (a
    /// pinned device it can't be pointed at falls back to the default).
    static func build(_ chosen: DevicePair, playback: PlaybackRing, gate: AudioGate,
                      onMic: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) throws
        -> (unit: VoiceUnitHandle, used: DevicePair) {
        var desc = AudioComponentDescription(
            componentType: kAudioUnitType_Output,
            componentSubType: kAudioUnitSubType_VoiceProcessingIO,
            componentManufacturer: kAudioUnitManufacturer_Apple,
            componentFlags: 0, componentFlagsMask: 0)
        guard let comp = AudioComponentFindNext(nil, &desc) else {
            throw VoiceIOError.audioUnit(step: "find VoiceProcessingIO", status: -1)
        }
        var maybeUnit: AudioUnit?
        try check("instantiate", AudioComponentInstanceNew(comp, &maybeUnit))
        guard let u = maybeUnit else { throw VoiceIOError.audioUnit(step: "instantiate", status: -1) }
        let vpio = VPIOUnit(unit: u)
        let ctx = VPIOContext(unit: u, playback: playback, gate: gate, onMic: onMic)
        do {
            var one: UInt32 = 1
            try setProp(u, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &one)
            try setProp(u, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Output, 0, &one)
            // Settings ▸ Voice ▸ Microphone / Speaker: before the formats and initialize.
            let used = AudioDevices.apply(vpio, chosen) { log.notice("\($0, privacy: .public)") }

            var fmt = AudioStreamBasicDescription(
                mSampleRate: 24_000,
                mFormatID: kAudioFormatLinearPCM,
                mFormatFlags: kAudioFormatFlagIsSignedInteger | kAudioFormatFlagIsPacked,
                mBytesPerPacket: 2, mFramesPerPacket: 1, mBytesPerFrame: 2,
                mChannelsPerFrame: 1, mBitsPerChannel: 16, mReserved: 0)
            // Mic data leaves input element 1 on its output scope; speaker data enters element 0.
            try setProp(u, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Output, 1, &fmt)
            try setProp(u, kAudioUnitProperty_StreamFormat, kAudioUnitScope_Input, 0, &fmt)

            // Keep other apps' audio as loud as possible while Bit listens. Best effort.
            var duck = AUVoiceIOOtherAudioDuckingConfiguration(
                mEnableAdvancedDucking: false, mDuckingLevel: .min) // ai-mascot engine.rs value
            _ = AudioUnitSetProperty(u, kAUVoiceIOProperty_OtherAudioDuckingConfiguration,
                                     kAudioUnitScope_Global, 0, &duck,
                                     UInt32(MemoryLayout<AUVoiceIOOtherAudioDuckingConfiguration>.size))

            let ref = Unmanaged.passRetained(ctx).toOpaque()
            ctx.retainedRef = ref
            var input = AURenderCallbackStruct(inputProc: vpioInputCallback, inputProcRefCon: ref)
            try setProp(u, kAudioOutputUnitProperty_SetInputCallback, kAudioUnitScope_Global, 1, &input)
            var render = AURenderCallbackStruct(inputProc: vpioRenderCallback, inputProcRefCon: ref)
            try setProp(u, kAudioUnitProperty_SetRenderCallback, kAudioUnitScope_Input, 0, &render)

            try check("initialize", AudioUnitInitialize(u))
            ctx.calibrateClock() // mach → Unix offset, computed once per start
            try check("start", AudioOutputUnitStart(u))
            vpio.context = ctx
            return (vpio, used)
        } catch {
            AudioUnitUninitialize(u)
            AudioComponentInstanceDispose(u)
            ctx.release()
            throw error
        }
    }

    /// The mic is the input element (bus 1), the speaker the output element (bus 0).
    func setCurrentDevice(_ dir: AudioDirection, id: UInt32) throws {
        var dev = AudioDeviceID(id)
        let st = AudioUnitSetProperty(unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global,
                                      dir == .input ? 1 : 0, &dev, UInt32(MemoryLayout<AudioDeviceID>.size))
        if st != noErr { throw AudioStatusError(status: st) }
    }

    func stop() {
        guard let ctx = context else { return }
        context = nil
        AudioOutputUnitStop(unit)  // synchronous: no callback runs after it returns
        AudioUnitUninitialize(unit)
        AudioComponentInstanceDispose(unit)
        ctx.release()
    }

    private static func setProp<T: BitwiseCopyable>(_ u: AudioUnit, _ id: AudioUnitPropertyID, _ scope: AudioUnitScope,
                                                    _ element: AudioUnitElement, _ value: inout T) throws {
        let status = AudioUnitSetProperty(u, id, scope, element, &value, UInt32(MemoryLayout<T>.size))
        try check("set property \(id) scope \(scope) element \(element)", status)
    }

    private static func check(_ step: String, _ status: OSStatus) throws {
        if status != noErr { throw VoiceIOError.audioUnit(step: step, status: status) }
    }
}

/// State the real-time callbacks reach through `inRefCon`. Everything is preallocated;
/// callbacks only read immutable fields, C atomics, and the rings.
final class VPIOContext: @unchecked Sendable {
    static let micCapacityFrames = 8192

    let unit: AudioUnit
    let playback: PlaybackRing
    /// Keeps the gate (and so `micEnabled`) alive while the callbacks run.
    let gate: AudioGate
    let micEnabled: UnsafeMutablePointer<Int64>
    let onMic: @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void
    let micBuf: UnsafeMutablePointer<Int16>
    let bufList: UnsafeMutablePointer<AudioBufferList>
    var retainedRef: UnsafeMutableRawPointer?
    // Set before AudioOutputUnitStart, read-only afterwards.
    private(set) var numer: UInt64 = 1
    private(set) var denom: UInt64 = 1
    private(set) var unixOffsetNanos: Int64 = 0

    init(unit: AudioUnit, playback: PlaybackRing, gate: AudioGate,
         onMic: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) {
        self.unit = unit
        self.playback = playback
        self.gate = gate
        micEnabled = gate.micOnSlot
        self.onMic = onMic
        micBuf = .allocate(capacity: Self.micCapacityFrames)
        micBuf.initialize(repeating: 0, count: Self.micCapacityFrames)
        bufList = .allocate(capacity: 1)
        bufList.initialize(to: AudioBufferList(
            mNumberBuffers: 1,
            mBuffers: AudioBuffer(mNumberChannels: 1, mDataByteSize: 0, mData: UnsafeMutableRawPointer(micBuf))))
    }

    deinit {
        micBuf.deallocate()
        bufList.deallocate()
    }

    /// Drops the reference handed to the callbacks (after the unit is disposed).
    func release() {
        if let ref = retainedRef {
            retainedRef = nil
            Unmanaged<VPIOContext>.fromOpaque(ref).release()
        }
    }

    func calibrateClock() {
        var tb = mach_timebase_info_data_t()
        mach_timebase_info(&tb)
        numer = UInt64(tb.numer)
        denom = UInt64(max(tb.denom, 1))
        let machNow = machToNanos(mach_absolute_time())
        let unixNow = Int64(Date().timeIntervalSince1970 * 1_000_000_000)
        unixOffsetNanos = unixNow - Int64(bitPattern: machNow)
    }

    @inline(__always)
    func machToNanos(_ t: UInt64) -> UInt64 {
        numer == denom ? t : t / denom * numer + (t % denom) * numer / denom
    }

    /// Unix nanos of the newest sample in a buffer whose first sample is at `ts`.
    @inline(__always)
    func newestSampleUnixNanos(_ ts: UnsafePointer<AudioTimeStamp>, frames: UInt32) -> UInt64 {
        let hostValid = ts.pointee.mFlags.contains(.hostTimeValid)
        let host = hostValid ? ts.pointee.mHostTime : mach_absolute_time()
        let span = hostValid && frames > 0 ? UInt64(frames - 1) * 1_000_000_000 / 24_000 : 0
        return UInt64(bitPattern: Int64(bitPattern: machToNanos(host) &+ span) &+ unixOffsetNanos)
    }
}

// MARK: - Real-time callbacks: no allocation, no locks, no Swift concurrency, no ObjC.

private func vpioInputCallback(
    _ refCon: UnsafeMutableRawPointer,
    _ flags: UnsafeMutablePointer<AudioUnitRenderActionFlags>,
    _ timeStamp: UnsafePointer<AudioTimeStamp>,
    _ bus: UInt32,
    _ frames: UInt32,
    _ ioData: UnsafeMutablePointer<AudioBufferList>?
) -> OSStatus {
    Unmanaged<VPIOContext>.fromOpaque(refCon)._withUnsafeGuaranteedRef { ctx in
        guard Int(frames) <= VPIOContext.micCapacityFrames else { return noErr }
        ctx.bufList.pointee.mNumberBuffers = 1
        ctx.bufList.pointee.mBuffers.mNumberChannels = 1
        ctx.bufList.pointee.mBuffers.mDataByteSize = frames * 2
        ctx.bufList.pointee.mBuffers.mData = UnsafeMutableRawPointer(ctx.micBuf)
        // Always pull the input so voice processing keeps running; deliver only when enabled.
        let status = AudioUnitRender(ctx.unit, flags, timeStamp, bus, frames, ctx.bufList)
        guard status == noErr, cdp_load(ctx.micEnabled) != 0 else { return status }
        ctx.onMic(UnsafeBufferPointer(start: ctx.micBuf, count: Int(frames)),
                  ctx.newestSampleUnixNanos(timeStamp, frames: frames))
        return noErr
    }
}

private func vpioRenderCallback(
    _ refCon: UnsafeMutableRawPointer,
    _ flags: UnsafeMutablePointer<AudioUnitRenderActionFlags>,
    _ timeStamp: UnsafePointer<AudioTimeStamp>,
    _ bus: UInt32,
    _ frames: UInt32,
    _ ioData: UnsafeMutablePointer<AudioBufferList>?
) -> OSStatus {
    guard let ioData else { return noErr }
    let buffer = ioData.pointee.mBuffers
    guard let data = buffer.mData else { return noErr }
    let count = min(Int(frames), Int(buffer.mDataByteSize) / 2)
    let out = UnsafeMutableBufferPointer(start: data.assumingMemoryBound(to: Int16.self), count: count)
    Unmanaged<VPIOContext>.fromOpaque(refCon)._withUnsafeGuaranteedRef { ctx in
        _ = ctx.playback.read(into: out)  // zero-fills the remainder
    }
    return noErr
}
