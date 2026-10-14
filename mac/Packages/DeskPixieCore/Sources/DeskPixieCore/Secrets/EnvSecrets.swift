import Foundation

/// Phase 3 secret lookup (Keychain arrives in Phase 4). Never log the returned value.
public enum EnvSecrets {
    public static let openAIKeyName = "OPENAI_API_KEY"

    /// `~/Library/Application Support/com.sihekuang.deskpixie/.env`
    public static var defaultDotenvURL: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/com.sihekuang.deskpixie/.env")
    }

    /// `OPENAI_API_KEY` from `env` when non-empty, else from the `KEY=value` lines of `dotenvURL`;
    /// nil when neither has a non-empty value.
    public static func openAIKey(env: [String: String], dotenvURL: URL) -> String? {
        if let v = env[openAIKeyName], !v.isEmpty { return v }
        guard let text = try? String(contentsOf: dotenvURL, encoding: .utf8) else { return nil }
        let v = parseDotenv(text)[openAIKeyName]
        return (v?.isEmpty ?? true) ? nil : v
    }

    /// Parses `KEY=value` lines: skips blank lines and `#` comments, drops an `export ` prefix,
    /// trims whitespace and strips one pair of surrounding single or double quotes. Later keys win.
    static func parseDotenv(_ text: String) -> [String: String] {
        var out: [String: String] = [:]
        for raw in text.split(whereSeparator: \.isNewline) {
            var line = raw.trimmingCharacters(in: .whitespaces)
            if line.isEmpty || line.hasPrefix("#") { continue }
            if line.hasPrefix("export ") { line = String(line.dropFirst("export ".count)) }
            guard let eq = line.firstIndex(of: "=") else { continue }
            let key = line[..<eq].trimmingCharacters(in: .whitespaces)
            var value = line[line.index(after: eq)...].trimmingCharacters(in: .whitespaces)
            if value.count >= 2, let f = value.first, let l = value.last, f == l, f == "\"" || f == "'" {
                value = String(value.dropFirst().dropLast())
            }
            if !key.isEmpty { out[key] = value }
        }
        return out
    }
}
