import DeskPixieCore
import Foundation
import Observation

/// Owns the one `ChatThreadModel` the chat window shows (#302). `EngineCoordinator` feeds it
/// the core's `chat.*` events once it exists; the window loads it when it opens.
@MainActor
enum ChatHub {
    /// The model, once the chat window or Pixie's badge has asked for it.
    private(set) static var shared: ChatThreadModel?

    /// The model for the app's engine, made on first use.
    static func model(for composition: CompositionRoot) -> ChatThreadModel {
        if let shared { return shared }
        let model = ChatThreadModel(client: EngineChatClient(engine: composition.engine))
        shared = model
        return model
    }
}

/// What the button above Pixie draws (#302): whether the chat window is shown, which
/// `ChatWindowController` keeps up to date, and the thread whose unread count puts the dot on it.
@MainActor
@Observable
final class ChatWindowPresence {
    var shown = false
    var model: ChatThreadModel?

    var toggle: ChatToggle { ChatToggle(shown: shown, unread: model?.unread ?? 0) }
}
