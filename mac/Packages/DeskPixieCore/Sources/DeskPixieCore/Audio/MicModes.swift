import AVFoundation

/// macOS's Mic Mode picker (Standard, Voice Isolation, Wide Spectrum) for this app's mic
/// (ai-mascot `crates/audio-macos/src/mic_modes.rs`). Voice Isolation keeps background voices
/// out better than any filter here; the modes apply while the mic is in use.
public enum MicModes {
    private static let selector = NSSelectorFromString("showSystemUserInterface:")

    /// Whether this macOS has the picker (a class method, macOS 12 and later).
    public static var available: Bool { (AVCaptureDevice.self as AnyObject).responds(to: selector) }

    /// For the port's test: the selector is a class method, not an instance method.
    static var isInstanceMethod: Bool { AVCaptureDevice.instancesRespond(to: selector) }

    /// Opens Control Center's Mic Mode picker; does nothing where it is not available.
    @MainActor
    public static func show() {
        guard available else { return }
        AVCaptureDevice.showSystemUserInterface(.microphoneModes)
    }
}
