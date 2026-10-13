// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "DeskPixieCore",
    platforms: [.macOS(.v14)],
    products: [
        .library(name: "DeskPixieCore", targets: ["DeskPixieCore"]),
    ],
    targets: [
        // Header-only C module: dp_* prototypes (hand-written copy of the
        // libdeskpixie C ABI) plus static-inline atomic helpers used by
        // PlaybackRing (Swift's Synchronization.Atomic needs macOS 15).
        .target(name: "CDP", path: "Sources/CDP", publicHeadersPath: "include"),
        // Zero-returning dp_* stubs, linked ONLY into the test target so the
        // app build resolves the real symbols from libdeskpixie.dylib.
        .target(name: "CDPStubs", path: "Sources/CDPStubs"),
        .target(name: "DeskPixieCore", dependencies: ["CDP"], path: "Sources/DeskPixieCore"),
        .testTarget(
            name: "DeskPixieCoreTests",
            dependencies: ["DeskPixieCore", "CDPStubs"],
            path: "Tests/DeskPixieCoreTests"
        ),
    ]
)
