import AppKit
import CoreGraphics
import DeskPixieCore

/// Whether the user is at the Mac (spec §4 "Away"): the screen is locked, the displays are
/// asleep, or there has been no keyboard/mouse input for `awayAfterMins`. Lock and display
/// sleep are notifications; idle time is polled every 5 s, so it alone still flips away/back
/// when a notification never arrives. `onChange(away)` fires only on a change.
@MainActor
final class PresenceMonitor {
    static let idlePollInterval: TimeInterval = 5

    /// true = away.
    var onChange: ((Bool) -> Void)?
    /// `notifications.awayAfterMins` (live).
    var awayAfterMins: Int = BubbleSettingsValues.defaults.awayAfterMins {
        didSet { evaluate() }
    }

    private(set) var away = false
    private var locked = false
    private var displaysAsleep = false
    private var timer: Timer?
    private var observers: [(NotificationCenter, NSObjectProtocol)] = []

    init() {
        let distributed = DistributedNotificationCenter.default()
        observe(distributed, "com.apple.screenIsLocked") { $0.locked = true }
        observe(distributed, "com.apple.screenIsUnlocked") { $0.locked = false }
        let workspace = NSWorkspace.shared.notificationCenter
        observe(workspace, NSWorkspace.screensDidSleepNotification.rawValue) { $0.displaysAsleep = true }
        observe(workspace, NSWorkspace.screensDidWakeNotification.rawValue) { $0.displaysAsleep = false }
        timer = Timer.scheduledTimer(withTimeInterval: Self.idlePollInterval, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.evaluate() }
        }
        RunLoop.main.add(timer!, forMode: .common)
    }

    func stop() {
        timer?.invalidate()
        timer = nil
        for (center, token) in observers { center.removeObserver(token) }
        observers = []
    }

    private func observe(_ center: NotificationCenter, _ name: String, _ update: @escaping @MainActor @Sendable (PresenceMonitor) -> Void) {
        let token = center.addObserver(forName: Notification.Name(name), object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                update(self)
                self.evaluate()
            }
        }
        observers.append((center, token))
    }

    /// Seconds since the last keyboard or mouse event in this login session.
    private static var idleSecs: TimeInterval {
        CGEventSource.secondsSinceLastEventType(.combinedSessionState, eventType: CGEventType(rawValue: ~0)!)
    }

    private func evaluate() {
        let now = PresenceRule.isAway(locked: locked, displaysAsleep: displaysAsleep, idleSecs: Self.idleSecs,
                                      awayAfterMins: awayAfterMins)
        guard now != away else { return }
        away = now
        onChange?(now)
    }
}
