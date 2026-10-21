import AppKit
import DeskPixieCore
import SwiftUI

/// Pixie drawn from a pack and animated by `PixiePlayer`, the way the desktop panel plays it: the
/// first-run window's welcome (idle), its pick-your-Pixie cards (#212) and done (happy, then idle)
/// (#149). Still with Reduce Motion on; the app icon when the pack can't be read.
struct PixieSpriteView: View {
    let state: String
    var scale: CGFloat = 4
    /// The pack's folder; nil: the bundled bit.
    var packDir: URL? = nil

    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var player = PixiePlayer()

    private static let bundledBit = (Bundle.main.resourceURL ?? Bundle.main.bundleURL).appendingPathComponent("pixies/bit")
    /// Read once each: the window redraws every frame.
    private static var loaded: [URL: (pack: Pack, sheet: SpriteSheet)?] = [:]

    private static func load(_ dir: URL) -> (pack: Pack, sheet: SpriteSheet)? {
        if let hit = loaded[dir] { return hit }
        let read = PackFolder.load(dir)
        loaded[dir] = read
        return read
    }

    var body: some View {
        Group {
            if let (pack, sheet) = Self.load(packDir ?? Self.bundledBit) {
                Canvas { ctx, _ in draw(ctx, pack: pack, sheet: sheet) }
                    .frame(width: CGFloat(pack.cellWidth) * scale, height: CGFloat(pack.cellHeight) * scale)
                    .task(id: "\(state) \(packDir?.path ?? "")") { await play(pack) }
            } else {
                Image(nsImage: NSApp.applicationIconImage)
                    .resizable()
                    .frame(width: 96, height: 96)
            }
        }
        .accessibilityHidden(true)
    }

    private func play(_ pack: Pack) async {
        player = PixiePlayer().setting(state, pack: pack)
        guard !reduceMotion, pack.fps > 0 else { return }
        while !Task.isCancelled {
            try? await Task.sleep(for: .seconds(1.0 / Double(pack.fps)))
            player = player.stepped(pack: pack)
        }
    }

    private func draw(_ ctx: GraphicsContext, pack: Pack, sheet: SpriteSheet) {
        let bob = CGFloat(player.bobOffset(pack: pack)) * scale
        if let body = sheet.crop(player.bodyRect(pack: pack)) {
            let size = CGSize(width: CGFloat(pack.cellWidth) * scale, height: CGFloat(pack.cellHeight) * scale)
            ctx.draw(Image(decorative: body, scale: 1).interpolation(.none), in: CGRect(origin: CGPoint(x: 0, y: bob), size: size))
        }
        let mouthRect = PixiePlayer.mouthRect(level: 0, pack: pack)
        if pack.mouth.levels > 0, let mouth = sheet.crop(mouthRect) {
            let at = player.mouthPos(pack: pack)
            ctx.draw(Image(decorative: mouth, scale: 1).interpolation(.none),
                     in: CGRect(x: at.x * scale, y: at.y * scale + bob,
                                width: mouthRect.width * scale, height: mouthRect.height * scale))
        }
    }
}
