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

    public init() {}

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
