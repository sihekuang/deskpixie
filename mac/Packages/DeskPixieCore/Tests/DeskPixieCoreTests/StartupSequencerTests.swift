import Foundation
import XCTest
@testable import DeskPixieCore

/// A core whose `configure` blocks (like a TCC prompt on `open()`) until the test releases it.
private final class BlockingEngine: CoreEngine, @unchecked Sendable {
    private let lock = NSLock()
    private let gate = DispatchSemaphore(value: 0)
    private var _calls: [String] = []
    private var _offMain: [Bool] = []
    var configureError: Error?

    var calls: [String] { lock.withLock { _calls } }
    var allOffMain: Bool { lock.withLock { !_offMain.isEmpty && !_offMain.contains(false) } }
    func release() { gate.signal() }

    private func record(_ name: String) {
        lock.withLock {
            _calls.append(name)
            _offMain.append(!Thread.isMainThread)
        }
    }

    func configure(_ c: EngineConfig) throws {
        record("configure")
        gate.wait()
        if let configureError { throw configureError }
    }

    func call(_ method: String, _ args: any Encodable) throws -> Data {
        record(method)
        switch method {
        case "settings.snapshot": return Data(#"{"values":{}}"#.utf8)
        case "app.state": return Data(#"{"state":"idle","statusLines":["Ready"]}"#.utf8)
        default: return Data("{}".utf8)
        }
    }

    func hotkey() {}
    func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool { true }
    func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool) { (0, false) }
    func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64) {}
    func micFailed(_ reason: String) {}
    func pollEvent() -> EngineEvent? { nil }
    func packJSON() -> Data? { nil }
    func shutdown() {}
}

@MainActor
final class StartupSequencerTests: XCTestCase {
    private func config() -> EngineConfig {
        EngineConfig.forBundle(resources: URL(fileURLWithPath: "/tmp/r"), appSupport: URL(fileURLWithPath: "/tmp/s"))
    }

    /// Waits (yielding the main actor) until `cond` holds; fails after `timeout`.
    private func waitUntil(_ timeout: TimeInterval = 3, _ cond: () -> Bool, file: StaticString = #filePath, line: UInt = #line) async {
        let end = Date().addingTimeInterval(timeout)
        while !cond() {
            if Date() > end { return XCTFail("timed out", file: file, line: line) }
            try? await Task.sleep(nanoseconds: 5_000_000)
        }
    }

    func testStatusLines() {
        XCTAssertEqual(StartupPhase.starting.statusLine, "Starting…")
        XCTAssertEqual(StartupPhase.waitingForPermission.statusLine, "Starting… waiting for macOS permission?")
        XCTAssertNotNil(StartupPhase.stalled.statusLine)
        XCTAssertNil(StartupPhase.ready.statusLine)
        XCTAssertNil(StartupPhase.failed("x").statusLine) // the engine error line covers it
        XCTAssertTrue(StartupPhase.starting.isStarting)
        XCTAssertTrue(StartupPhase.stalled.isStarting)
        XCTAssertFalse(StartupPhase.ready.isStarting)
    }

    func testBlockedConfigureKeepsMainActorFreeAndShowsStartingThenReady() async {
        let engine = BlockingEngine()
        let seq = StartupSequencer(permissionHintAfter: .milliseconds(60), stalledAfter: .milliseconds(150))
        var phases: [StartupPhase] = []
        seq.onPhase = { phases.append($0) }
        let migrated = OrderLog()
        let config = config()

        let run = Task { @MainActor in
            await seq.run(engine: engine, config: { config }, migrate: { migrated.add(Thread.isMainThread ? 0 : 1) })
        }
        // The main actor keeps running while configure is blocked.
        await waitUntil { engine.calls == ["configure"] }
        XCTAssertEqual(seq.phase, .starting)
        XCTAssertTrue(seq.phase.isStarting)
        await waitUntil { seq.phase == .waitingForPermission }
        await waitUntil { seq.phase == .stalled }

        engine.release()
        let result = await run.value
        XCTAssertEqual(seq.phase, .ready)
        XCTAssertEqual(phases, [.starting, .waitingForPermission, .stalled, .ready])
        XCTAssertNil(result.configureError)
        XCTAssertNotNil(result.snapshot)
        XCTAssertEqual(result.appState?.statusLines, ["Ready"])
        XCTAssertEqual(engine.calls, ["configure", "settings.snapshot", "app.state"])
        XCTAssertTrue(engine.allOffMain, "every core call ran off the main thread")
        XCTAssertEqual(migrated.values, [1], "migration ran once, off the main thread")
    }

    func testFastConfigureNeverShowsPermissionHint() async {
        let engine = BlockingEngine()
        engine.release()
        let seq = StartupSequencer(permissionHintAfter: .seconds(5), stalledAfter: .seconds(10))
        var phases: [StartupPhase] = []
        seq.onPhase = { phases.append($0) }
        let config = config()
        _ = await seq.run(engine: engine, config: { config }, migrate: {})
        XCTAssertEqual(phases, [.starting, .ready])
        // The timer was cancelled: no late hint overwrites .ready.
        try? await Task.sleep(nanoseconds: 50_000_000)
        XCTAssertEqual(seq.phase, .ready)
    }

    /// The first-run gate (#149) looks for settings.json in `migrate`: that must happen before the
    /// core is configured, since configuring writes the file.
    func testMigrationRunsBeforeTheCoreIsConfigured() async {
        let engine = BlockingEngine()
        engine.release()
        let seq = StartupSequencer()
        let config = config()
        let callsAtMigrate = OrderLog()
        _ = await seq.run(engine: engine, config: { config }, migrate: { callsAtMigrate.add(engine.calls.count) })
        XCTAssertEqual(callsAtMigrate.values, [0], "nothing reached the core before migration")
        XCTAssertEqual(engine.calls.first, "configure")
    }

    func testConfigureFailureSkipsReadsAndReportsFailed() async {
        let engine = BlockingEngine()
        engine.configureError = DeskPixieEngineError.configureFailed("bad paths")
        engine.release()
        let seq = StartupSequencer()
        let config = config()
        let result = await seq.run(engine: engine, config: { config }, migrate: {})
        XCTAssertEqual(result.configureError, "bad paths")
        XCTAssertNil(result.snapshot)
        XCTAssertNil(result.appState)
        XCTAssertEqual(engine.calls, ["configure"])
        XCTAssertEqual(seq.phase, .failed("bad paths"))
    }

    func testCoreWorkerRunsOffMainInOrder() async {
        let worker = CoreWorker(label: "test.core")
        let order = OrderLog()
        async let a: Bool? = worker.run { order.add(1); return !Thread.isMainThread }
        async let b: Bool? = worker.run { order.add(2); return !Thread.isMainThread }
        let (offA, offB) = await (a, b)
        XCTAssertEqual(offA, true); XCTAssertEqual(offB, true)
        XCTAssertEqual(order.values.sorted(), [1, 2])
        // Submission order is kept for calls made from one task.
        for i in 3...6 { _ = await worker.run { order.add(i) } }
        XCTAssertEqual(Array(order.values.suffix(4)), [3, 4, 5, 6])
    }

    func testTrayDisablesSettingsWhileStarting() {
        let items = TrayMenu.build(nil, localStatusLines: [StartupPhase.starting.statusLine!],
                                   micModeAvailable: false, starting: true)
        XCTAssertEqual(items.first, .label("Starting…"))
        guard case .action(let settings)? = items.first(where: {
            if case .action(let r) = $0 { return r.id == TrayMenu.settingsID }
            return false
        }) else { return XCTFail("no Settings row") }
        XCTAssertFalse(settings.enabled)
        let ready = TrayMenu.build(nil, localStatusLines: [], micModeAvailable: false)
        XCTAssertTrue(ready.contains(.action(TrayMenu.Row(id: TrayMenu.settingsID, title: "Settings…", shortcut: ","))))
    }
}

private final class OrderLog: @unchecked Sendable {
    private let lock = NSLock()
    private var _values: [Int] = []
    var values: [Int] { lock.withLock { _values } }
    func add(_ v: Int) { lock.withLock { _values.append(v) } }
}
