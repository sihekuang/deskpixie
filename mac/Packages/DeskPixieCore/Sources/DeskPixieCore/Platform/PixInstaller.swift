import Foundation
import Observation

/// Where the pix command link stands (#89).
public enum PixLinkState: Equatable, Sendable {
    case notInstalled, installed
    /// A link DeskPixie made that no longer reaches this app's pix (the app moved, or another copy).
    case broken
    /// Something else is at ~/.local/bin/pix (described); DeskPixie leaves it alone.
    case other(String)
}

public enum PixInstallError: Error, LocalizedError, Equatable {
    case occupied(String)
    public var errorDescription: String? {
        switch self {
        case .occupied(let what): return "~/.local/bin/pix is \(what); DeskPixie won't replace it."
        }
    }
}

/// Installs the pix command as a symlink to the app's bundled copy, so it updates with the app.
/// It touches nothing but ~/.local/bin/pix.
public struct PixInstaller: Sendable {
    public let link: URL
    public let bundled: URL
    private let path: String

    /// The line that puts ~/.local/bin on PATH, for ~/.zshrc.
    public static let pathLine = "export PATH=\"$HOME/.local/bin:$PATH\""
    /// Optional aliases so plain `claude` and `codex` go through pix.
    public static let aliases = "alias claude='pix claude'\nalias codex='pix codex'"

    /// path is the login shell's PATH (a GUI app's own PATH is launchd's, not the shell's).
    public init(home: URL, bundled: URL, path: String) {
        self.link = home.appendingPathComponent(".local/bin/pix")
        self.bundled = bundled
        self.path = path
    }

    /// The app's own installer: this app's Helpers/pix.
    public static func forThisApp(path: String) -> PixInstaller {
        PixInstaller(home: FileManager.default.homeDirectoryForCurrentUser,
                     bundled: Bundle.main.bundleURL.appendingPathComponent("Contents/Helpers/pix"),
                     path: path)
    }

    public var onPath: Bool {
        let dir = link.deletingLastPathComponent().standardizedFileURL.path
        return path.split(separator: ":").contains {
            URL(fileURLWithPath: String($0)).standardizedFileURL.path == dir
        }
    }

    public func state() -> PixLinkState {
        let fm = FileManager.default
        guard let dest = try? fm.destinationOfSymbolicLink(atPath: link.path) else {
            return fm.fileExists(atPath: link.path) ? .other("a file") : .notInstalled
        }
        guard Self.isOurs(dest) || dest == bundled.path else { return .other("a link to \(dest)") }
        return dest == bundled.path && fm.fileExists(atPath: dest) ? .installed : .broken
    }

    public func install() throws {
        let fm = FileManager.default
        switch state() {
        case .installed: return
        case .other(let what): throw PixInstallError.occupied(what)
        case .broken: try fm.removeItem(at: link)
        case .notInstalled: break
        }
        try fm.createDirectory(at: link.deletingLastPathComponent(), withIntermediateDirectories: true)
        try fm.createSymbolicLink(atPath: link.path, withDestinationPath: bundled.path)
    }

    public func uninstall() throws {
        switch state() {
        case .notInstalled: return
        case .other(let what): throw PixInstallError.occupied(what)
        case .installed, .broken: try FileManager.default.removeItem(at: link)
        }
    }

    /// A link DeskPixie made points at some DeskPixie app's Helpers/pix.
    private static func isOurs(_ dest: String) -> Bool { dest.hasSuffix(".app/Contents/Helpers/pix") }

    /// The login shell's PATH (2 s at most), or "" when it can't be read.
    public static func loginShellPath() -> String {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh")
        p.arguments = ["-lc", "printf %s \"$PATH\""]
        let out = Pipe()
        p.standardOutput = out
        p.standardError = FileHandle.nullDevice
        p.standardInput = FileHandle.nullDevice
        guard (try? p.run()) != nil else { return "" }
        let deadline = Date().addingTimeInterval(2)
        while p.isRunning && Date() < deadline { usleep(20_000) }
        if p.isRunning { p.terminate(); return "" }
        return String(data: out.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
    }
}

/// The Settings row's state for the pix command.
@Observable
public final class PixCommandModel {
    public private(set) var state: PixLinkState = .notInstalled
    public private(set) var error: String?
    public let onPath: Bool
    private let installer: PixInstaller

    public init(installer: PixInstaller) {
        self.installer = installer
        self.onPath = installer.onPath
        refresh()
    }

    public var stateText: String {
        switch state {
        case .installed: return "Installed at ~/.local/bin/pix"
        case .notInstalled: return "Not installed"
        case .broken: return "Link broken (the app moved). Install fixes it."
        case .other(let what): return "~/.local/bin/pix is \(what); left alone"
        }
    }

    public func refresh() { state = installer.state() }

    public func install() { run(installer.install) }
    public func uninstall() { run(installer.uninstall) }

    private func run(_ action: () throws -> Void) {
        do {
            try action()
            error = nil
        } catch {
            self.error = error.localizedDescription
        }
        refresh()
    }
}
