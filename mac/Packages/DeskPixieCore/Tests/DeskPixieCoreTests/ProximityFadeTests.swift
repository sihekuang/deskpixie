import CoreGraphics
import XCTest
@testable import DeskPixieCore

final class ProximityFadeTests: XCTestCase {
    private let frame = CGRect(x: 100, y: 100, width: 256, height: 256)
    private let radius: CGFloat = 150

    private func opacity(_ cursor: CGPoint, enabled: Bool = true) -> CGFloat {
        ProximityFade.opacity(spriteFrame: frame, cursor: cursor, fadeRadius: radius, enabled: enabled)
    }

    func testCursorInsideTheFrameIsTheFloor() {
        XCTAssertEqual(opacity(CGPoint(x: 200, y: 200)), ProximityFade.minOpacity)
        XCTAssertEqual(ProximityFade.minOpacity, 0.15)
    }

    func testFarCursorIsFullyOpaque() {
        XCTAssertEqual(opacity(CGPoint(x: frame.maxX + radius, y: 200)), 1.0)
        XCTAssertEqual(opacity(CGPoint(x: 5000, y: -5000)), 1.0)
    }

    func testDisabledIsFullyOpaque() {
        XCTAssertEqual(opacity(CGPoint(x: 200, y: 200), enabled: false), 1.0)
    }

    func testAt65PercentOfRadiusIsQuadratic() {
        let expected = pow((0.65 - 0.3) / 0.7, 2)
        XCTAssertEqual(opacity(CGPoint(x: frame.maxX + radius * 0.65, y: 200)), expected, accuracy: 1e-6)
        XCTAssertEqual(expected, 0.25, accuracy: 1e-6)
        // Distance is to the nearest point of the frame, also diagonally from a corner.
        let d = radius * 0.65 / sqrt(2)
        XCTAssertEqual(opacity(CGPoint(x: frame.minX - d, y: frame.minY - d)), expected, accuracy: 1e-6)
    }

    func testMonotonicOverTheRadius() {
        var last: CGFloat = -1
        for i in 0...300 {
            let o = opacity(CGPoint(x: frame.maxX + radius * CGFloat(i) / 300, y: 200))
            XCTAssertGreaterThanOrEqual(o, last)
            XCTAssertGreaterThanOrEqual(o, ProximityFade.minOpacity)
            XCTAssertLessThanOrEqual(o, 1.0)
            last = o
        }
    }
}
