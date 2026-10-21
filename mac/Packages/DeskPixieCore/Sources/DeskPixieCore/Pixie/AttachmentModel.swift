import Foundation

/// One attachment waiting for the next request (#240), as the core's `attachments` event lists it.
public struct AttachmentItem: Codable, Sendable, Equatable, Identifiable {
    public let id: String
    public let name: String
    /// "image" or "file".
    public let kind: String
    public let path: String
    public let size: Int

    public init(id: String, name: String, kind: String, path: String, size: Int) {
        self.id = id; self.name = name; self.kind = kind; self.path = path; self.size = size
    }
}

/// The chips above the text box (or in the card under Pixie in voice mode).
public struct AttachmentModel: Equatable, Sendable {
    public private(set) var items: [AttachmentItem] = []
    /// The last refusal ("That file is over 2 MB."), shown on the hint row until the next try.
    public var notice: String?

    public init() {}

    public mutating func apply(_ items: [AttachmentItem]) { self.items = items }

    public var chipsVisible: Bool { !items.isEmpty }
}
