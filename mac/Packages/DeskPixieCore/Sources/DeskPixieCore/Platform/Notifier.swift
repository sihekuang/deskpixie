import AppKit
import UserNotifications

/// Abstraction so tests don't touch UNUserNotificationCenter or play sounds.
@MainActor
public protocol NotificationSink: AnyObject {
    func post(title: String, body: String) async
    func playGlass()
}

/// Handles the engine's `notify` event: system notification + optional Glass sound.
/// Wiring: the coordinator forwards events with `notifier.handle(event)`.
@MainActor
public final class Notifier {
    private let sink: NotificationSink
    private var pending: [Task<Void, Never>] = []

    public init(sink: NotificationSink = SystemNotificationSink()) {
        self.sink = sink
    }

    public func handle(_ event: EngineEvent) {
        guard case let .notify(title, body, sound) = event else { return }
        if sound == "glass" { sink.playGlass() }
        let sink = self.sink
        pending.append(Task { await sink.post(title: title, body: body) })
    }

    /// Await all in-flight posts (tests).
    func drain() async {
        let tasks = pending
        pending = []
        for t in tasks { await t.value }
    }
}

@MainActor
public final class SystemNotificationSink: NotificationSink {
    private var authorization: Task<Bool, Never>?

    /// Built once at launch (the coordinator's Notifier); installs the foreground presenter so
    /// banners show while DeskPixie is frontmost.
    public init() {
        ForegroundPresenter.install(on: UNUserNotificationCenter.current())
    }

    private func authorized() async -> Bool {
        if let authorization { return await authorization.value }
        let t = Task {
            (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert])) ?? false
        }
        authorization = t
        return await t.value
    }

    public func post(title: String, body: String) async {
        guard await authorized() else { return }
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        // Sound is played separately (Glass.aiff) so it is independent of the user's notification sound setting.
        let req = UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil)
        try? await UNUserNotificationCenter.current().add(req)
    }

    public func playGlass() {
        NSSound(contentsOfFile: "/System/Library/Sounds/Glass.aiff", byReference: true)?.play()
    }
}

/// macOS only presents a notification from the frontmost app when the center's delegate asks
/// for it. DeskPixie is frontmost whenever the Settings window or the canvas has focus, which is
/// exactly when an agent's question or a timer must not go unseen. The center holds its delegate
/// weakly, so the shared instance keeps it alive.
public final class ForegroundPresenter: NSObject, UNUserNotificationCenterDelegate, Sendable {
    public static let shared = ForegroundPresenter()

    /// Banner plus Notification Center. No `.sound`: the Glass chime is played separately.
    public static let options: UNNotificationPresentationOptions = [.banner, .list]

    @MainActor
    public static func install(on center: UNUserNotificationCenter) {
        center.delegate = shared
    }

    public func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler(Self.options)
    }
}
