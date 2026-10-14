import Foundation

/// One-shot copy of the pre-rename "AI Mascot" data folder into DeskPixie's
/// (spec §6.2). Idempotent; safe to call on every launch. Must run before
/// anything reads the data folder.
///
/// - runs only when the new folder is missing/empty and the old one exists
/// - copies (never moves) so an older build still finds its data
/// - copies into a hidden temp sibling, then renames into place
/// - symlinks are copied as links
/// - `agent.session` in the copy's settings.json becomes `deskpixie`, only
///   when it is exactly `ai-mascot`; unparsable JSON is left untouched
public enum DataMigration {
    public static let oldFolder = "com.sihekuang.aimascot"
    public static let newFolder = "com.sihekuang.deskpixie"

    public enum Outcome: Equatable, Sendable {
        case migrated
        case skipped
        case failed(String)
    }

    @discardableResult
    public static func run(appSupport: URL, pid: Int32 = ProcessInfo.processInfo.processIdentifier) -> Outcome {
        let fm = FileManager.default
        let old = appSupport.appendingPathComponent(oldFolder)
        let new = appSupport.appendingPathComponent(newFolder)

        var isDir: ObjCBool = false
        guard fm.fileExists(atPath: old.path, isDirectory: &isDir), isDir.boolValue else { return .skipped }
        if fm.fileExists(atPath: new.path) {
            guard let items = try? fm.contentsOfDirectory(atPath: new.path), items.isEmpty else { return .skipped }
        }

        let tmp = appSupport.appendingPathComponent(".\(newFolder).migrating-\(pid)")
        do {
            try? fm.removeItem(at: tmp)
            try copyTree(from: old, to: tmp)
            rewriteSession(in: tmp.appendingPathComponent("settings.json"))
            if fm.fileExists(atPath: new.path) { try fm.removeItem(at: new) }  // empty folder
            try fm.moveItem(at: tmp, to: new)
            return .migrated
        } catch {
            try? fm.removeItem(at: tmp)
            return .failed(error.localizedDescription)
        }
    }

    /// Migration against the real `~/Library/Application Support`.
    @discardableResult
    public static func runIfNeeded() -> Outcome {
        guard let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first else {
            return .skipped
        }
        return run(appSupport: base)
    }

    private static func copyTree(from src: URL, to dst: URL) throws {
        let fm = FileManager.default
        try fm.createDirectory(at: dst, withIntermediateDirectories: true)
        for name in try fm.contentsOfDirectory(atPath: src.path) {
            let s = src.appendingPathComponent(name)
            let d = dst.appendingPathComponent(name)
            let type = try fm.attributesOfItem(atPath: s.path)[.type] as? FileAttributeType
            switch type {
            case .typeSymbolicLink:
                try fm.createSymbolicLink(atPath: d.path, withDestinationPath: try fm.destinationOfSymbolicLink(atPath: s.path))
            case .typeDirectory:
                try copyTree(from: s, to: d)
            default:
                try fm.copyItem(at: s, to: d)
            }
        }
    }

    private static func rewriteSession(in file: URL) {
        guard let data = try? Data(contentsOf: file),
              var root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              var agent = root["agent"] as? [String: Any],
              agent["session"] as? String == "ai-mascot" else { return }
        agent["session"] = "deskpixie"
        root["agent"] = agent
        guard let out = try? JSONSerialization.data(withJSONObject: root, options: [.prettyPrinted, .sortedKeys]) else { return }
        try? out.write(to: file, options: .atomic)
    }
}
