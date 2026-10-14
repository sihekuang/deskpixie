import Foundation

/// A serial background queue for core calls that may touch the disk, the network or spawn a
/// process (`app.action`, `settings.choices`, `canvas.geometry`, …). Keeps them off the main
/// thread and in submission order; callers hop back to the main actor for UI updates.
///
/// Shutdown: `shutdown()` (or `close()` then `drain()`) stops accepting work and waits for
/// everything already accepted, so no handler runs during or after the engine's teardown.
/// Work submitted after `close()` is dropped (`run` returns nil).
public final class CoreWorker: @unchecked Sendable {
    private let queue: DispatchQueue
    private let key = DispatchSpecificKey<Bool>()
    private let lock = NSLock()
    private var closed = false

    public init(label: String = "com.sihekuang.deskpixie.core") {
        queue = DispatchQueue(label: label, qos: .userInitiated)
        queue.setSpecific(key: key, value: true)
    }

    /// True once `close()` has been called.
    public var isClosed: Bool { lock.withLock { closed } }

    /// True when called from a work item on this worker's queue.
    public var isOnWorker: Bool { DispatchQueue.getSpecific(key: key) == true }

    /// Enqueues `work` unless the worker is closed. The check and the enqueue happen under one
    /// lock, so anything accepted is enqueued before `close()` returns and `drain()` covers it.
    @discardableResult
    private func submit(_ work: @escaping @Sendable () -> Void) -> Bool {
        lock.withLock {
            guard !closed else { return false }
            queue.async(execute: work)
            return true
        }
    }

    /// Runs `work` on the queue and returns its value; nil when the worker is closed (the work
    /// is not run).
    @discardableResult
    public func run<T: Sendable>(_ work: @escaping @Sendable () -> T) async -> T? {
        await withCheckedContinuation { (cont: CheckedContinuation<T?, Never>) in
            let accepted = submit { cont.resume(returning: work()) }
            if !accepted { cont.resume(returning: nil) }
        }
    }

    /// Fire and forget; dropped when the worker is closed.
    public func async(_ work: @escaping @Sendable () -> Void) {
        submit(work)
    }

    /// Blocks until all work enqueued so far has run. Called from the worker's own queue it
    /// returns at once (waiting there would deadlock: the caller is the work being waited for).
    public func drain() {
        guard !isOnWorker else { return }
        queue.sync {}
    }

    /// Stops accepting work. Already-accepted work still runs; pair with `drain()`.
    public func close() {
        lock.withLock { closed = true }
    }

    /// `close()` then `drain()`: after it returns, no work runs on this worker again.
    public func shutdown() {
        close()
        drain()
    }
}
