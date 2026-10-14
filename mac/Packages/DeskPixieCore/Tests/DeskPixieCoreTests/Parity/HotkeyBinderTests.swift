import Carbon
import XCTest
@testable import DeskPixieCore

/// V-030: the coordinator's hotkey glue (EngineCoordinator delegates to `HotkeyBinder`):
/// bind, rollback to the previous combo when the new one is taken, and the
/// "Hotkey unavailable: <formatted>" tray line.
@MainActor
final class HotkeyBinderTests: XCTestCase {
    private final class FakeRegistrar: HotkeyRegistrar {
        var taken: Set<UInt32> = []
        var registered: (keyCode: UInt32, modifiers: UInt32)?
        var starts: [UInt32] = []
        var onPress: (@MainActor () -> Void)?

        func start(keyCode: UInt32, modifiers: UInt32, onPress: @escaping @MainActor () -> Void) throws {
            starts.append(keyCode)
            registered = nil // CarbonHotkey.start drops the old binding first
            if taken.contains(keyCode) { throw CarbonHotkey.Failure.register(-9878) }
            registered = (keyCode, modifiers)
            self.onPress = onPress
        }

        func stop() { registered = nil }
    }

    private var presses = 0

    private func binder(_ r: FakeRegistrar) -> HotkeyBinder {
        HotkeyBinder(registrar: r, platform: .macos) { [unowned self] in self.presses += 1 }
    }

    func testLaunchBindsTheStoredCombo() {
        let r = FakeRegistrar()
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        XCTAssertEqual(b.boundCombo, "ctrl+alt+Space")
        XCTAssertNil(b.unavailableLine)
        XCTAssertEqual(r.registered?.keyCode, UInt32(kVK_Space))
        XCTAssertEqual(r.registered?.modifiers, UInt32(controlKey | optionKey))
        r.onPress?()
        XCTAssertEqual(presses, 1, "a press runs talk")
    }

    func testLaunchFailureShowsTheFormattedTrayLine() {
        let r = FakeRegistrar()
        r.taken = [UInt32(kVK_Space)]
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        XCTAssertNil(b.boundCombo)
        XCTAssertEqual(b.unavailableLine, "Hotkey unavailable: ⌃⌥Space")
    }

    func testUnparseableComboIsUnavailableAndKeepsTheOldBinding() {
        let r = FakeRegistrar()
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        XCTAssertFalse(b.bind("ctrl+Nope", previous: nil))
        XCTAssertEqual(b.boundCombo, "ctrl+alt+Space", "nothing was unbound")
        XCTAssertEqual(r.starts.count, 1)
    }

    func testRebindRollsBackToThePreviousCombo() {
        let r = FakeRegistrar()
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        r.taken = [UInt32(kVK_ANSI_K)]
        XCTAssertFalse(b.bind("ctrl+alt+K", previous: nil))
        XCTAssertEqual(b.boundCombo, "ctrl+alt+Space", "the old combo is bound again")
        XCTAssertEqual(r.registered?.keyCode, UInt32(kVK_Space))
        XCTAssertEqual(r.starts, [UInt32(kVK_Space), UInt32(kVK_ANSI_K), UInt32(kVK_Space)])
        XCTAssertNil(b.unavailableLine, "the old combo still works: no tray line")
    }

    func testSuccessfulRebindClearsTheTrayLine() {
        let r = FakeRegistrar()
        r.taken = [UInt32(kVK_Space)]
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        XCTAssertNotNil(b.unavailableLine)
        XCTAssertTrue(b.bind("ctrl+alt+K", previous: nil))
        XCTAssertEqual(b.boundCombo, "ctrl+alt+K")
        XCTAssertNil(b.unavailableLine)
    }

    func testHotkeyEventFailureReportsToTheCoreAndKeepsTheOldCombo() {
        let r = FakeRegistrar()
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        r.taken = [UInt32(kVK_ANSI_K)]
        var failed: [String] = []
        b.changed("ctrl+alt+K") { failed.append($0) }
        XCTAssertEqual(failed, ["ctrl+alt+K"], "settings.hotkeyFailed so the core does not adopt it")
        XCTAssertEqual(b.boundCombo, "ctrl+alt+Space")
        XCTAssertNil(b.unavailableLine)
        b.changed("ctrl+alt+Space") { failed.append($0) }
        XCTAssertEqual(failed.count, 1, "the bound combo is a no-op")
    }

    func testHotkeyEventFailureWithNothingBoundShowsTheTrayLine() {
        let r = FakeRegistrar()
        r.taken = [UInt32(kVK_Space), UInt32(kVK_ANSI_K)]
        let b = binder(r)
        b.bindAtLaunch("ctrl+alt+Space")
        b.changed("ctrl+shift+K") { _ in }
        XCTAssertNil(b.boundCombo)
        XCTAssertEqual(b.unavailableLine, "Hotkey unavailable: ⌃⇧K")
    }

    func testCarbonHotkeyIsARegistrar() {
        let r: any HotkeyRegistrar = CarbonHotkey()
        XCTAssertNotNil(r)
    }
}
