import Foundation

/// Where DeskPixie's data folder lives. Production always uses the real
/// `~/Library/Application Support/<DataMigration.newFolder>`; `DESKPIXIE_SUPPORT_DIR`
/// is read only to point an isolated instance (an e2e test run) at a throwaway
/// directory instead, so it never shares `pix.sock`, the herdr/tmux session, or
/// settings.json with the real, already-running app.
public enum AppSupportDirectory {
    public static func resolve(
        home: URL,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) -> URL {
        if let override = environment["DESKPIXIE_SUPPORT_DIR"], !override.isEmpty {
            return URL(fileURLWithPath: override, isDirectory: true)
        }
        return home.appendingPathComponent("Library/Application Support/\(DataMigration.newFolder)", isDirectory: true)
    }
}
