import AppKit
import os
import UserNotifications

/// Abstraction so tests don't touch UNUserNotificationCenter or play sounds.
@MainActor
public protocol NotificationSink: AnyObject {
    func post(title: String, body: String) async
    /// A notification about a bubble line (#199): clicked, it does what a click on the line does.
    func post(title: String, body: String, update: UpdateLine) async
    func playGlass()
}

extension NotificationSink {
    public func post(title: String, body: String, update: UpdateLine) async {
        await post(title: title, body: body)
    }
}

/// What a notification says (#199): a bubble line's short form when it is about one (its title,
/// else the event's; its brief, else its text), as the bubble shows it.
public enum NotificationText {
    public static func of(title: String, body: String, update: UpdateLine?) -> (title: String, body: String) {
        guard let update else { return (title, body) }
        let t = update.title.flatMap { $0.isEmpty ? nil : $0 } ?? title
        return (t, update.shownText)
    }
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
        guard case let .notify(title, body, sound, update) = event else { return }
        if sound == "glass" { sink.playGlass() }
        let sink = self.sink
        let text = NotificationText.of(title: title, body: body, update: update)
        pending.append(Task {
            if let update {
                await sink.post(title: text.title, body: text.body, update: update)
            } else {
                await sink.post(title: title, body: body)
            }
        })
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
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "notify")
    private var authorization: Task<Bool, Never>?

    /// Built once at launch (the coordinator's Notifier); installs the foreground presenter so
    /// banners show while DeskPixie is frontmost.
    public init() {
        ForegroundPresenter.install(on: UNUserNotificationCenter.current())
    }

    private func authorized() async -> Bool {
        if let authorization { return await authorization.value }
        let t = Task {
            do {
                let ok = try await UNUserNotificationCenter.current().requestAuthorization(options: [.alert])
                if !ok { Self.log.error("notifications not allowed") }
                return ok
            } catch {
                Self.log.error("notification authorization: \(String(describing: error), privacy: .public)")
                return false
            }
        }
        authorization = t
        return await t.value
    }

    public func post(title: String, body: String) async {
        guard await authorized() else { return }
        let req = UNNotificationRequest(identifier: UUID().uuidString, content: Self.content(title: title, body: body),
                                        trigger: nil)
        await Self.add(req)
    }

    public func post(title: String, body: String, update: UpdateLine) async {
        guard await authorized() else { return }
        let content = Self.content(title: title, body: body)
        if let data = try? JSONEncoder().encode(update), let json = String(data: data, encoding: .utf8) {
            content.userInfo = [Self.updateKey: json]
        }
        let req = UNNotificationRequest(identifier: Self.identifier(update: update.id), content: content, trigger: nil)
        await Self.add(req)
    }

    /// The notification of bubble line `id`: removed when the line is dismissed in the bubble.
    public nonisolated static func identifier(update id: UInt64) -> String { "update-\(id)" }

    /// userInfo key holding the bubble line (JSON).
    public nonisolated static let updateKey = "update"

    /// The line a notification carries, if any.
    public nonisolated static func update(from userInfo: [AnyHashable: Any]) -> UpdateLine? {
        guard let json = userInfo[updateKey] as? String else { return nil }
        return try? JSONDecoder().decode(UpdateLine.self, from: Data(json.utf8))
    }

    /// The lines left the bubble (clicked or cleared): their notifications go too.
    public nonisolated static func removeNotifications(updates ids: [UInt64]) {
        guard !ids.isEmpty else { return }
        UNUserNotificationCenter.current().removeDeliveredNotifications(withIdentifiers: ids.map { identifier(update: $0) })
    }

    private static func add(_ req: UNNotificationRequest) async {
        do {
            try await UNUserNotificationCenter.current().add(req)
        } catch {
            log.error("notification: \(String(describing: error), privacy: .public)")
        }
    }

    /// The chime for `sound: "glass"` (MacNotifier's `afplay` file).
    public static let glassPath = "/System/Library/Sounds/Glass.aiff"

    /// The notification for a `notify` event. No sound: Glass is played separately, so it does
    /// not depend on the user's notification sound setting.
    public nonisolated static func content(title: String, body: String) -> UNMutableNotificationContent {
        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        return content
    }

    public func playGlass() {
        NSSound(contentsOfFile: Self.glassPath, byReference: true)?.play()
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

    /// A click on a notification about a bubble line (#199): set by the app to do what a click
    /// on the line does. A click before it is set (the app still starting) waits for it.
    @MainActor public static var onOpenUpdate: ((UpdateLine) -> Void)? {
        didSet {
            guard let onOpenUpdate, let line = waitingOpen else { return }
            waitingOpen = nil
            onOpenUpdate(line)
        }
    }
    @MainActor private static var waitingOpen: UpdateLine?

    @MainActor static func open(_ line: UpdateLine) {
        if let onOpenUpdate { onOpenUpdate(line) } else { waitingOpen = line }
    }

    public func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        if response.actionIdentifier == UNNotificationDefaultActionIdentifier,
           let line = SystemNotificationSink.update(from: response.notification.request.content.userInfo) {
            Task { @MainActor in Self.open(line) }
        }
        completionHandler()
    }

    public func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler(Self.options)
    }
}
