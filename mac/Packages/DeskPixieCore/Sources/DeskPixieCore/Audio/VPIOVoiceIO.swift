import AVFoundation
import AudioToolbox
import CDP
import Darwin
import Foundation

/// One `kAudioUnitSubType_VoiceProcessingIO` unit (echo cancellation) for both the mic
/// (input element 1) and the speaker (output element 0), 24 kHz mono Int16 on both sides.
/// Port of ai-mascot `crates/audio-macos/src/engine.rs` `build()`. System default devices
/// (no device pinning in Phase 3). `init` has no side effects: permission is requested
/// and the unit is built only in `start`.
public final class VPIOVoiceIO: VoiceIO, @unchecked Sendable {
    private let lock = NSLock()
    private var unit: AudioUnit?
    private var context: VPIOContext?
    // Mic gate survives start/stop: slot 0 = 1 when enabled.
    private let micEnabled: UnsafeMutablePointer<Int64>

    public init() {
        micEnabled = .allocate(capacity: 1)
        micEnabled.initialize(to: 0)
    }

    deinit {
        stop()
        micEnabled.deallocate()
    }

    public func start(playback: PlaybackRing,
                      onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) async throws {
        try await Self.requireMicPermission()
        try startUnit(playback: playback, onMicSamples: onMicSamples)
    }

    public func setMicEnabled(_ on: Bool) {
        cdp_store(micEnabled, on ? 1 : 0)
    }

    public func stop() {
        lock.lock()
        let u = unit
        let ctx = context
        unit = nil
        context = nil
        lock.unlock()
        guard let u else { return }
        AudioOutputUnitStop(u)  // synchronous: no callback runs after it returns
        AudioUnitUninitialize(u)
        AudioComponentInstanceDispose(u)
        ctx?.release()
    }

    // MARK: - Setup

    private static func requireMicPermission() async throws {
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            return
        case .notDetermined:
            if await AVCaptureDevice.requestAccess(for: .audio) { return }
            throw VoiceIOError.permissionDenied
        default:
            throw VoiceIOError.permissionDenied
        }
    }

    private func startUnit(playback: PlaybackRing,
                           onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) throws {
        stop()
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

        let ctx = VPIOContext(unit: u, playback: playback, micEnabled: micEnabled, onMic: onMicSamples)
        do {
            var one: UInt32 = 1
            try setProp(u, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Input, 1, &one)
            try setProp(u, kAudioOutputUnitProperty_EnableIO, kAudioUnitScope_Output, 0, &one)

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
        } catch {
            AudioUnitUninitialize(u)
            AudioComponentInstanceDispose(u)
            ctx.release()
            throw error
        }
        lock.lock()
        unit = u
        context = ctx
        lock.unlock()
    }

    private func setProp<T: BitwiseCopyable>(_ u: AudioUnit, _ id: AudioUnitPropertyID, _ scope: AudioUnitScope,
                            _ element: AudioUnitElement, _ value: inout T) throws {
        let status = AudioUnitSetProperty(u, id, scope, element, &value, UInt32(MemoryLayout<T>.size))
        try check("set property \(id) scope \(scope) element \(element)", status)
    }

    private func check(_ step: String, _ status: OSStatus) throws {
        if status != noErr { throw VoiceIOError.audioUnit(step: step, status: status) }
    }
}

/// State the real-time callbacks reach through `inRefCon`. Everything is preallocated;
/// callbacks only read immutable fields, C atomics, and the rings.
final class VPIOContext: @unchecked Sendable {
    static let micCapacityFrames = 8192

    let unit: AudioUnit
    let playback: PlaybackRing
    let micEnabled: UnsafeMutablePointer<Int64>
    let onMic: @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void
    let micBuf: UnsafeMutablePointer<Int16>
    let bufList: UnsafeMutablePointer<AudioBufferList>
    var retainedRef: UnsafeMutableRawPointer?
    // Set before AudioOutputUnitStart, read-only afterwards.
    private(set) var numer: UInt64 = 1
    private(set) var denom: UInt64 = 1
    private(set) var unixOffsetNanos: Int64 = 0

    init(unit: AudioUnit, playback: PlaybackRing, micEnabled: UnsafeMutablePointer<Int64>,
         onMic: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) {
        self.unit = unit
        self.playback = playback
        self.micEnabled = micEnabled
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
