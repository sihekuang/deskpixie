import DeskPixieCore
import Foundation

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
