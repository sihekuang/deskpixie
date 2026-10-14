import AppKit
import DeskPixieCore

/// The connected displays from `NSScreen`, in desktop coordinates (top-left of the primary
/// display, y down): Rust's `TauriDisplays`. AppKit frames are already logical points on every
/// display, so mixed Retina/1x setups need no rescaling. A snapshot taken on the main actor at
/// init; the layout functions then read plain values.
struct ScreenDisplays: LayoutDisplays {
    private let monitors: [Layout.Monitor]
    private let primary: Layout.Area?

    @MainActor
    init() {
        let top = Self.primaryMaxY
        monitors = NSScreen.screens.map {
            Layout.Monitor(frame: DesktopSpace.area($0.frame, primaryMaxY: top),
                           work: DesktopSpace.area($0.visibleFrame, primaryMaxY: top))
        }
        primary = NSScreen.screens.first.map { DesktopSpace.area($0.frame, primaryMaxY: top) }
    }

    /// The primary display (the one with the menu bar, origin 0,0) is always `screens[0]`.
    @MainActor
    static var primaryMaxY: CGFloat { NSScreen.screens.first?.frame.maxY ?? 0 }

    func monitorList() -> [Layout.Monitor] { monitors }

    func primaryFrame() -> Layout.Area? { primary }

    /// A window frame (AppKit) in desktop coordinates.
    @MainActor
    static func desktopRect(_ frame: NSRect) -> CGRect {
        DesktopSpace.desktopRect(frame, primaryMaxY: primaryMaxY)
    }

    /// The AppKit frame for a window of `size` whose desktop top-left is `topLeft`.
    @MainActor
    static func appKitFrame(topLeft: CGPoint, size: CGSize) -> NSRect {
        NSRect(origin: DesktopSpace.appKitOrigin(desktopTopLeft: topLeft, size: size, primaryMaxY: primaryMaxY),
               size: size)
    }
}
