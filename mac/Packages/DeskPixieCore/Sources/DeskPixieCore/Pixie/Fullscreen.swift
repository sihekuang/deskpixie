import CoreGraphics

/// "A fullscreen app is in front" (the updates bubble then shows only its badge). The shell
/// passes the frontmost app's on-screen, layer-0 window bounds (`CGWindowListCopyWindowInfo`)
/// and the screens' frames, all in global display coordinates (top-left origin). Not
/// `NSMenu.menuBarVisible()`: that is false all the time with an auto-hiding menu bar.
public enum Fullscreen {
    /// True when one of `windows` covers one of `screens` exactly (within `tolerance` points).
    public static func isFullscreen(windows: [CGRect], screens: [CGRect], tolerance: CGFloat = 1) -> Bool {
        windows.contains { w in
            screens.contains { s in
                abs(w.minX - s.minX) <= tolerance && abs(w.minY - s.minY) <= tolerance
                    && abs(w.maxX - s.maxX) <= tolerance && abs(w.maxY - s.maxY) <= tolerance
            }
        }
    }
}
