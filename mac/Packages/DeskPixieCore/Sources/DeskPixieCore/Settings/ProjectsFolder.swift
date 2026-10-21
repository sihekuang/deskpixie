import Foundation

/// `setup.projectsFolder` (#216): the projects folder setting checked on disk (Go
/// `settings.ProjectsFolder`). A missing folder below home is created when the agent starts;
/// one that can't be used or created leaves workers in DeskPixie's own folder (`fallback`).
public struct ProjectsFolderStatus: Decodable, Equatable, Sendable {
    public enum State: String, Decodable, Sendable {
        case ready, missing, unusable
    }

    public let state: State
    /// The setting's folder as a full path ("" when the setting can't be read as one).
    public let path: String
    /// DeskPixie's own projects folder.
    public let fallback: String
    /// One line for the window.
    public let detail: String

    public init(state: State, path: String, fallback: String, detail: String) {
        self.state = state
        self.path = path
        self.fallback = fallback
        self.detail = detail
    }

    /// `path` as shown: the home folder as ~.
    public static func shown(_ path: String, home: String) -> String {
        guard !home.isEmpty, path == home || path.hasPrefix(home + "/") else { return path }
        return "~" + path.dropFirst(home.count)
    }
}
