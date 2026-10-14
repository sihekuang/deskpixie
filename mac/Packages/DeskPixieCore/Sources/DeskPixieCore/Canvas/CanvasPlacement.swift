import CoreGraphics
import Foundation

/// A canvas window frame as settings `canvas.window` stores it and `canvas.geometry` takes it:
/// logical points, top-left origin at the primary display's top-left, y down (Tauri's space, so
/// geometry saved by ai-mascot keeps working). `x`/`y` is the frame's (title bar's) top-left,
/// `width`/`height` the content size.
public struct CanvasGeometry: Codable, Equatable, Sendable {
    public var x: Double
    public var y: Double
    public var width: Double
    public var height: Double

    public init(x: Double, y: Double, width: Double, height: Double) {
        self.x = x
        self.y = y
        self.width = width
        self.height = height
    }
}

/// A work area in the same top-left, y-down space.
public struct CanvasRect: Equatable, Sendable {
    public var x: Double
    public var y: Double
    public var w: Double
    public var h: Double

    public init(x: Double, y: Double, w: Double, h: Double) {
        self.x = x
        self.y = y
        self.w = w
        self.h = h
    }
}

/// Window placement rules (port of `src-tauri/src/canvas/window.rs`).
public enum CanvasPlacement {
    /// At least this many points of the window must be on a work area, both ways.
    public static let minVisible = 100.0
    public static let minSize = CGSize(width: 480, height: 360)
    public static let defaultSize = CGSize(width: 960, height: 720)
    /// The geometry is saved once the window has been still this long.
    public static let settleDelay: Duration = .milliseconds(500)

    /// Geometry that is safe to remember (not a parked or minimized window).
    public static func plausible(_ g: CanvasGeometry) -> Bool {
        [g.x, g.y, g.width, g.height].allSatisfy(\.isFinite)
            && g.x > -10_000 && g.y > -10_000 && g.width >= 100 && g.height >= 100
    }

    /// The saved geometry if it overlaps a work area by `minVisible` points, with the size
    /// clamped to that area and the title bar kept on screen; nil = use the centred default.
    public static func place(_ saved: CanvasGeometry, areas: [CanvasRect]) -> CanvasGeometry? {
        guard plausible(saved) else { return nil }
        guard let area = areas.first(where: { a in
            let w = min(saved.x + saved.width, a.x + a.w) - max(saved.x, a.x)
            let h = min(saved.y + saved.height, a.y + a.h) - max(saved.y, a.y)
            return w >= minVisible && h >= minVisible
        }) else { return nil }
        let width = max(min(saved.width, area.w), min(Double(minSize.width), area.w))
        let height = max(min(saved.height, area.h), min(Double(minSize.height), area.h))
        return CanvasGeometry(x: saved.x, y: max(saved.y, area.y), width: width, height: height)
    }
}

/// AppKit (bottom-left origin, y up) ↔ the top-left, y-down space of `CanvasGeometry`.
public enum CanvasScreenSpace {
    /// `primaryMaxY` is `NSScreen.screens[0].frame.maxY`.
    public static func topLeft(_ r: CGRect, primaryMaxY: Double) -> CanvasRect {
        CanvasRect(x: r.minX, y: primaryMaxY - r.maxY, w: r.width, h: r.height)
    }

    /// The AppKit frame origin of a frame whose top-left is (`topLeftX`, `topLeftY`).
    public static func appKitOrigin(topLeftX: Double, topLeftY: Double, frameHeight: Double,
                                    primaryMaxY: Double) -> CGPoint {
        CGPoint(x: topLeftX, y: primaryMaxY - topLeftY - frameHeight)
    }
}

/// The canvas window title.
public enum CanvasWindowTitle {
    /// "Canvas — <title>"; "Canvas" for an empty title (the empty state). A title that is
    /// already formatted (the core may send either) is kept.
    public static func make(_ title: String) -> String {
        let t = title.trimmingCharacters(in: .whitespacesAndNewlines)
        if t.isEmpty || t == "Canvas" || t.hasPrefix("Canvas — ") { return t.isEmpty ? "Canvas" : t }
        return "Canvas — \(t)"
    }
}

/// Reads the saved window geometry (`values.canvas.window`) from a `settings.snapshot` reply.
public enum CanvasSettingsValues {
    private struct Snapshot: Decodable {
        struct Values: Decodable { var canvas: Canvas? }
        struct Canvas: Decodable { var window: CanvasGeometry? }
        var values: Values?
    }

    public static func window(fromSnapshot data: Data) -> CanvasGeometry? {
        (try? JSONDecoder().decode(Snapshot.self, from: data))?.values?.canvas?.window
    }
}
