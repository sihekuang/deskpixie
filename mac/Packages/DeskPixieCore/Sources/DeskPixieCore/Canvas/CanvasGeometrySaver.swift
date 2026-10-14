import Foundation

/// Saves canvas geometry (`canvas.geometry`) through the core worker so every save runs in
/// order: settled saves are queued on the worker; the close / quit save drains the worker
/// first (earlier settled saves land before it) and then saves synchronously.
public struct CanvasGeometrySaver: Sendable {
    private let worker: CoreWorker
    private let save: @Sendable (CanvasGeometry) -> Void

    public init(worker: CoreWorker, save: @escaping @Sendable (CanvasGeometry) -> Void) {
        self.worker = worker
        self.save = save
    }

    /// The window has been still: save off the caller's thread, after earlier saves.
    public func saveSettled(_ g: CanvasGeometry) {
        let save = save
        worker.async { save(g) }
    }

    /// Window closing or app quitting: wait for queued saves, then save on this thread.
    public func saveNow(_ g: CanvasGeometry) {
        worker.drain()
        save(g)
    }
}
