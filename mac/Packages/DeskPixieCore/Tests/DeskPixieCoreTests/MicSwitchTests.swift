import Foundation
import XCTest
@testable import DeskPixieCore

/// Port of ai-mascot `crates/audio-macos/src/mic.rs` tests (8).
final class MicSwitchTests: XCTestCase {
    final class FakeAuth: MicAuthorizer, @unchecked Sendable {
        private let lock = NSLock()
        private var _status: MicAccess
        private var _pending: [@Sendable (Bool) -> Void] = []

        init(_ status: MicAccess) { _status = status }

        var requests: Int { lock.withLock { _pending.count } }

        func status() -> MicAccess { lock.withLock { _status } }
        func request(_ done: @escaping @Sendable (Bool) -> Void) { lock.withLock { _pending.append(done) } }

        /// The user answers the latest prompt.
        func answer(_ granted: Bool) {
            let done = lock.withLock { () -> @Sendable (Bool) -> Void in
                _status = granted ? .granted : .denied
                return _pending.removeLast()
            }
            done(granted)
        }

        /// The earliest prompt, removed (to answer it out of order).
        func takeFirst() -> @Sendable (Bool) -> Void { lock.withLock { _pending.removeFirst() } }
    }

    /// A conversation's event channel.
    final class Channel: @unchecked Sendable {
        private let lock = NSLock()
        private var events: [MicEvent] = []
        var sink: @Sendable (MicEvent) -> Void { { [self] e in lock.withLock { events.append(e) } } }
        /// Like `try_recv`: the oldest event, or nil.
        func tryRecv() -> MicEvent? { lock.withLock { events.isEmpty ? nil : events.removeFirst() } }
    }

    final class Counter: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        var value: Int { lock.withLock { n } }
        func bump() { lock.withLock { n += 1 } }
    }

    struct Rig {
        let mic: MicSwitch
        let state: MicState
        let auth: FakeAuth
        let started: Counter
    }

    func rig(_ status: MicAccess) -> Rig {
        let state = MicState()
        let auth = FakeAuth(status)
        let started = Counter()
        let mic = MicSwitch(state: state, auth: auth, onStarted: { started.bump() })
        return Rig(mic: mic, state: state, auth: auth, started: started)
    }

    func test_granted_turns_the_mic_on_at_once() throws {
        let r = rig(.granted)
        let ch = Channel()
        XCTAssertEqual(try r.mic.start(events: ch.sink), .live)
        XCTAssertTrue(r.state.isOn)
        XCTAssertEqual(r.started.value, 1)
        XCTAssertEqual(r.auth.requests, 0, "no prompt when already granted")
    }

    func test_denied_fails_the_start() {
        let r = rig(.denied)
        let ch = Channel()
        XCTAssertThrowsError(try r.mic.start(events: ch.sink)) {
            XCTAssertEqual($0 as? AudioFailure, .permissionDenied)
        }
        XCTAssertFalse(r.state.isOn)
        XCTAssertEqual(r.started.value, 0)
    }

    func test_not_determined_asks_first_and_starts_once_granted() throws {
        let r = rig(.notDetermined)
        let ch = Channel()
        XCTAssertEqual(try r.mic.start(events: ch.sink), .awaitingPermission)
        XCTAssertEqual(r.auth.requests, 1, "asked up front")
        XCTAssertFalse(r.state.isOn, "off while the prompt shows")
        XCTAssertEqual(r.started.value, 0)
        XCTAssertNil(ch.tryRecv())
        r.auth.answer(true)
        XCTAssertEqual(ch.tryRecv(), .live, "the conversation hears the mic is on")
        XCTAssertTrue(r.state.isOn)
        XCTAssertEqual(r.started.value, 1)
    }

    func test_not_determined_then_denied_fails_the_conversation() throws {
        let r = rig(.notDetermined)
        let ch = Channel()
        _ = try r.mic.start(events: ch.sink)
        r.auth.answer(false)
        XCTAssertEqual(ch.tryRecv(), .failed(.permissionDenied))
        XCTAssertFalse(r.state.isOn)
        XCTAssertEqual(r.started.value, 0)
    }

    func test_an_answer_after_stop_is_ignored() throws {
        let r = rig(.notDetermined)
        let ch = Channel()
        _ = try r.mic.start(events: ch.sink)
        r.mic.stop()
        r.auth.answer(true)
        XCTAssertFalse(r.state.isOn, "the conversation ended while the prompt showed")
        XCTAssertEqual(r.started.value, 0)
        XCTAssertNil(ch.tryRecv())
    }

    func test_an_answer_for_an_earlier_start_is_ignored() throws {
        let r = rig(.notDetermined)
        let ch1 = Channel()
        _ = try r.mic.start(events: ch1.sink)
        r.mic.stop()
        let ch2 = Channel()
        _ = try r.mic.start(events: ch2.sink)
        // The first prompt's answer comes back (denied); the second is still showing.
        let first = r.auth.takeFirst()
        first(false)
        XCTAssertNil(ch1.tryRecv())
        XCTAssertNil(ch2.tryRecv(), "a stale answer doesn't fail the new conversation")
        r.auth.answer(true)
        XCTAssertTrue(r.state.isOn)
    }

    func test_stop_turns_off_and_forgets_the_conversation() throws {
        let r = rig(.granted)
        let ch = Channel()
        _ = try r.mic.start(events: ch.sink)
        r.state.send(.failed(.deviceChanged))
        XCTAssertNotNil(ch.tryRecv())
        r.mic.stop()
        XCTAssertFalse(r.state.isOn)
        r.state.send(.failed(.deviceChanged))
        XCTAssertNil(ch.tryRecv())
    }

    func test_mute_keeps_the_conversation_to_report_to() throws {
        let r = rig(.granted)
        let ch = Channel()
        _ = try r.mic.start(events: ch.sink)
        r.state.mute()
        XCTAssertFalse(r.state.isOn)
        r.state.send(.failed(.deviceChanged))
        XCTAssertNotNil(ch.tryRecv())
    }
}
