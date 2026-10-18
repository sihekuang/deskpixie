import Foundation

/// A pack folder on disk (`pixie.json` + its sheet, e.g. the bundled `pixies/bit`), read whole.
/// A pack made before the pixie rename (#68) has only `mascot.json`, read when `pixie.json` is
/// absent.
public enum PackFolder {
    /// The manifest file names: the current one first, then its pre-#68 name.
    public static let manifestNames = ["pixie.json", "mascot.json"]

    /// The pack and its decoded sheet; nil when the manifest or the sheet can't be read.
    public static func load(_ dir: URL) -> (pack: Pack, sheet: SpriteSheet)? {
        guard let json = manifest(in: dir),
              let pack = try? JSONDecoder().decode(Pack.self, from: json) else { return nil }
        let sheetName = (try? JSONDecoder().decode(SheetName.self, from: json))?.sheet ?? "sheet.png"
        guard let sheet = SpriteSheet(url: dir.appendingPathComponent(sheetName)) else { return nil }
        return (pack, sheet)
    }

    private struct SheetName: Decodable { var sheet: String? }

    /// The bytes of the first manifest in `dir` that exists (see `manifestNames`).
    static func manifest(in dir: URL) -> Data? {
        for name in manifestNames {
            if let data = try? Data(contentsOf: dir.appendingPathComponent(name)) { return data }
        }
        return nil
    }
}
