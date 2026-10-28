import AppKit
import Quartz

/// Quick Look for the files a message of yours went with (#394): the ones clicked, opened at
/// the one clicked; the arrow keys go through the rest.
@MainActor
final class SentPreview: NSObject, QLPreviewPanelDataSource {
    private(set) var urls: [URL] = []

    func show(_ paths: [String], at index: Int) {
        urls = paths.map { URL(fileURLWithPath: $0) }
        guard let panel = QLPreviewPanel.shared() else { return }
        if panel.isVisible {
            panel.reloadData()
        } else {
            panel.makeKeyAndOrderFront(nil)
        }
        panel.currentPreviewItemIndex = index
    }

    nonisolated func numberOfPreviewItems(in panel: QLPreviewPanel!) -> Int {
        MainActor.assumeIsolated { urls.count }
    }

    nonisolated func previewPanel(_ panel: QLPreviewPanel!, previewItemAt index: Int) -> (any QLPreviewItem)! {
        let url = MainActor.assumeIsolated { urls.indices.contains(index) ? urls[index] : nil }
        return url as NSURL?
    }
}

/// The chat window, which controls the Quick Look panel while it has files to show.
final class ChatWindow: NSWindow {
    var preview: SentPreview?

    override func acceptsPreviewPanelControl(_ panel: QLPreviewPanel!) -> Bool {
        preview?.urls.isEmpty == false
    }

    override func beginPreviewPanelControl(_ panel: QLPreviewPanel!) {
        panel.dataSource = preview
    }

    override func endPreviewPanelControl(_ panel: QLPreviewPanel!) {
        panel.dataSource = nil
    }
}
