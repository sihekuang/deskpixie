import AppKit
import DeskPixieCore
import Observation
import os
import UniformTypeIdentifiers

/// The attachments waiting for the next request (#240), on the shell's side: the chips' model,
/// the hint row ("Screenshot copied · Attach"), and every way in (the hint, Cmd-V, a drop, 📎,
/// Pixie's take_clipboard). The core keeps the files; this hands it what to add. Observable, so
/// the chat window's box (#302) redraws with the same chips as the box under Pixie.
@MainActor
@Observable
final class AttachmentsController {
    nonisolated private static let log = Logger(subsystem: "com.sihekuang.deskpixie", category: "attachments")

    private let engine: CoreEngine
    private let worker: CoreWorker
    let clipboard: ClipboardSource
    private let onChange: () -> Void
    private(set) var model = AttachmentModel()
    /// What the clipboard holds now (by its types), and the copy already attached or sent past.
    private var kind = ClipboardKind.none
    private var seenCount = -1
    private var usedCount = -1

    init(engine: CoreEngine, worker: CoreWorker, clipboard: ClipboardSource, onChange: @escaping () -> Void) {
        self.engine = engine
        self.worker = worker
        self.clipboard = clipboard
        self.onChange = onChange
        fetch()
    }

    /// The hint row's words while the clipboard holds something not yet attached.
    var hint: String? { usedCount == seenCount ? nil : ClipboardRules.hint(kind) }

    /// The core's `attachments` event.
    func apply(_ items: [AttachmentItem]) {
        model.apply(items)
        onChange()
    }

    /// Looks at the clipboard's types when it changed (cheap; never a read). True on a new copy.
    @discardableResult
    func poll() -> Bool {
        let count = clipboard.changeCount
        guard count != seenCount else { return false }
        seenCount = count
        kind = ClipboardRules.kind(clipboard.detectedTypes())
        if model.notice != nil { model.notice = nil }
        onChange()
        return kind != .none
    }

    var clipboardKind: ClipboardKind { kind }

    /// The copy on the clipboard now was already attached (or sent past): nothing to offer.
    var currentUsed: Bool { usedCount == seenCount }

    /// The message went: the hint is for a newer copy only.
    func sent() { usedCount = seenCount }

    /// The Attach row, Cmd-V, the bubble: what was copied, read now.
    func attachClipboard() {
        poll()
        usedCount = seenCount
        add(AttachmentIntake.fromClipboard(clipboard))
    }

    /// Pixie's take_clipboard: read, add, then tell the core how it went.
    func takeClipboard(request: String) {
        poll()
        usedCount = seenCount
        let result = AttachmentIntake.fromClipboard(clipboard)
        Task { @MainActor in
            var error: String?
            switch result {
            case .refused(let why): error = why
            case .adds(let adds): error = await self.send(adds)
            }
            let engine = self.engine, done = ClipboardDone(request: request, error: error)
            self.worker.async { _ = try? engine.call("clipboard.done", done) }
        }
    }

    func remove(_ id: String) {
        let engine = engine
        worker.async { _ = try? engine.call("attachments.remove", RemoveArgs(id: id)) }
    }

    /// 📎: pick files.
    func pick() {
        let open = NSOpenPanel()
        open.allowsMultipleSelection = true
        open.canChooseDirectories = false
        open.prompt = "Attach"
        NSApp.activate()
        open.begin { [weak self] response in
            guard response == .OK else { return }
            let urls = open.urls
            MainActor.assumeIsolated { self?.add(.adds(urls.map { .file($0.path) })) }
        }
    }

    /// Files or images dropped on the box or on Pixie.
    func drop(_ providers: [NSItemProvider]) -> Bool {
        var took = false
        for p in providers {
            if p.hasItemConformingToTypeIdentifier(UTType.fileURL.identifier) {
                took = true
                _ = p.loadObject(ofClass: URL.self) { [weak self] url, _ in
                    guard let path = url?.path else { return }
                    Task { @MainActor in self?.add(.adds([.file(path)])) }
                }
            } else if p.hasItemConformingToTypeIdentifier(UTType.image.identifier) {
                took = true
                p.loadDataRepresentation(forTypeIdentifier: UTType.image.identifier) { [weak self] data, _ in
                    guard let data else { return }
                    Task { @MainActor in self?.add(AttachmentIntake.fromImage(data)) }
                }
            }
        }
        return took
    }

    /// Files or an image dropped on Pixie (a drop is the user's own act: no prompt).
    func drop(board: NSPasteboard) -> Bool {
        let urls = board.readObjects(forClasses: [NSURL.self], options: [.urlReadingFileURLsOnly: true]) as? [URL] ?? []
        if !urls.isEmpty {
            add(.adds(urls.map { .file($0.path) }))
            return true
        }
        guard let image = NSImage(pasteboard: board), let tiff = image.tiffRepresentation else { return false }
        add(AttachmentIntake.fromImage(tiff))
        return true
    }

    private func add(_ result: IntakeResult) {
        switch result {
        case .refused(let why):
            model.notice = why
            onChange()
        case .adds(let adds):
            Task { @MainActor in
                self.model.notice = await self.send(adds)
                self.onChange()
            }
        }
    }

    /// Hands each to the core; the first refusal's words, or nil.
    private func send(_ adds: [AttachmentAdd]) async -> String? {
        let engine = engine
        let failure: String?? = await worker.run {
            var first: String?
            for a in adds {
                do {
                    switch a {
                    case .data(let name, let data):
                        _ = try engine.call("attachments.add", AddArgs(name: name, data: data.base64EncodedString(), path: nil))
                    case .file(let path):
                        _ = try engine.call("attachments.add", AddArgs(name: nil, data: nil, path: path))
                    }
                } catch DeskPixieEngineError.callFailed(let why) {
                    first = first ?? why
                } catch {
                    first = first ?? "Couldn't attach that."
                }
            }
            return first
        }
        return failure ?? "Couldn't attach that."
    }

    private func fetch() {
        let engine = engine
        Task { @MainActor [weak self] in
            let items: [AttachmentItem]?? = await self?.worker.run {
                try? engine.call("attachments.list", as: ListReply.self).items
            }
            guard let self, let items = items ?? nil, !items.isEmpty else { return }
            self.apply(items)
        }
    }

    private struct AddArgs: Encodable, Sendable { let name: String?; let data: String?; let path: String? }
    private struct RemoveArgs: Encodable, Sendable { let id: String }
    private struct ClipboardDone: Encodable, Sendable { let request: String; let error: String? }
    private struct ListReply: Decodable { var items: [AttachmentItem] }
}
