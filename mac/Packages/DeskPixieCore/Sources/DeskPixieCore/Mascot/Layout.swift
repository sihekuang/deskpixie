import CoreGraphics

/// The connected displays, in desktop coordinates (`DesktopSpace`). The app implements it with
/// `NSScreen`; tests use a fake (layout.rs `Displays`).
public protocol LayoutDisplays {
    func monitorList() -> [Layout.Monitor]
    /// The primary display's full frame.
    func primaryFrame() -> Layout.Area?
}

/// Window geometry helpers, a port of ai-mascot `src-tauri/src/layout.rs`.
///
/// All values are logical points in *desktop coordinates*: origin at the primary display's
/// top-left, y growing downwards (tao/Rust's convention, and the one saved as `position` in
/// settings.json). `DesktopSpace` converts from and to AppKit's bottom-left, y-up space.
public enum Layout {
    /// A display's area.
    public struct Area: Equatable, Sendable {
        public var x, y, w, h: Double

        public init(x: Double, y: Double, w: Double, h: Double) {
            self.x = x
            self.y = y
            self.w = w
            self.h = h
        }

        func contains(_ p: CGPoint) -> Bool {
            p.x >= x && p.x < x + w && p.y >= y && p.y < y + h
        }

        func distance(to p: CGPoint) -> Double {
            let dx = max(x - p.x, p.x - (x + w), 0)
            let dy = max(y - p.y, p.y - (y + h), 0)
            return (dx * dx + dy * dy).squareRoot()
        }
    }

    /// A connected display: its whole frame, and its work area (minus the menu bar and the Dock).
    public struct Monitor: Equatable, Sendable {
        public var frame: Area
        public var work: Area

        public init(frame: Area, work: Area) {
            self.frame = frame
            self.work = work
        }
    }

    /// Rust `Shared::window_size`: one cell at `scale` (0 → the default ×8, as config.rs normalizes).
    public static func windowSize(pack: Pack, scale: Int) -> CGSize {
        let s = scale > 0 ? scale : 8
        return CGSize(width: pack.cellWidth * s, height: pack.cellHeight * s)
    }

    /// New top-left so the window's bottom-centre (Bit's feet) stays put when resizing.
    public static func keepFeet(_ pos: CGPoint, old: CGSize, new: CGSize) -> CGPoint {
        CGPoint(x: pos.x + (old.width - new.width) / 2, y: pos.y + old.height - new.height)
    }

    /// Bottom-right corner of the primary display, inset 40 pt from the right and 80 pt from the
    /// bottom (clear of the Dock); (100, 100) without display information.
    public static func defaultPosition(primary: Area?, size: CGSize) -> CGPoint {
        guard let m = primary else { return CGPoint(x: 100, y: 100) }
        return CGPoint(x: m.x + m.w - size.width - 40, y: m.y + m.h - size.height - 80)
    }

    /// Where the mascot window opens. A saved position stays as long as the window's centre is
    /// on a connected display's frame (over the Dock or the menu bar counts: the user put Bit
    /// there). One left on an unplugged display is pulled fully into the nearest work area.
    public static func restorePosition(_ saved: CGPoint?, size: CGSize, displays: LayoutDisplays) -> CGPoint {
        let monitors = displays.monitorList()
        guard let saved else {
            return defaultPosition(primary: displays.primaryFrame() ?? monitors.first?.frame, size: size)
        }
        let centre = CGPoint(x: saved.x + size.width / 2, y: saved.y + size.height / 2)
        if monitors.isEmpty || monitors.contains(where: { $0.frame.contains(centre) }) {
            return saved
        }
        guard let m = monitors.map(\.work).min(by: { $0.distance(to: centre) < $1.distance(to: centre) }) else {
            return saved
        }
        func clamp(_ v: Double, _ lo: Double, _ span: Double, _ len: Double) -> Double {
            max(min(v, lo + span - len), lo)
        }
        return CGPoint(x: clamp(saved.x, m.x, m.w, size.width), y: clamp(saved.y, m.y, m.h, size.height))
    }
}

/// Converts between AppKit's global space (origin at the primary display's bottom-left, y up)
/// and desktop coordinates (origin at its top-left, y down). `primaryMaxY` is the primary
/// display's `frame.maxY` (its height, since its origin is 0,0).
public enum DesktopSpace {
    public static func desktopPoint(_ p: CGPoint, primaryMaxY: CGFloat) -> CGPoint {
        CGPoint(x: p.x, y: primaryMaxY - p.y)
    }

    public static func desktopRect(_ r: CGRect, primaryMaxY: CGFloat) -> CGRect {
        CGRect(x: r.minX, y: primaryMaxY - r.maxY, width: r.width, height: r.height)
    }

    public static func area(_ r: CGRect, primaryMaxY: CGFloat) -> Layout.Area {
        let d = desktopRect(r, primaryMaxY: primaryMaxY)
        return Layout.Area(x: d.minX, y: d.minY, w: d.width, h: d.height)
    }

    /// The AppKit frame origin of a window of `size` whose desktop top-left is `desktopTopLeft`.
    public static func appKitOrigin(desktopTopLeft p: CGPoint, size: CGSize, primaryMaxY: CGFloat) -> CGPoint {
        CGPoint(x: p.x, y: primaryMaxY - p.y - size.height)
    }
}
