import XCTest
@testable import DeskPixieCore

final class PlaybackRingTests: XCTestCase {
    private func write(_ r: PlaybackRing, _ a: [Int16]) -> Int {
        a.withUnsafeBufferPointer { r.write($0) }
    }
    private func read(_ r: PlaybackRing, _ n: Int) -> ([Int16], Int) {
        var out = [Int16](repeating: 7, count: n)
        let c = out.withUnsafeMutableBufferPointer { r.read(into: $0) }
        return (out, c)
    }

    func testWriteThenRead() {
        let r = PlaybackRing(capacity: 64)
        XCTAssertEqual(write(r, [1, 2, 3, 4]), 4)
        XCTAssertEqual(r.buffered, 4)
        let (out, c) = read(r, 4)
        XCTAssertEqual(c, 4)
        XCTAssertEqual(out, [1, 2, 3, 4])
        XCTAssertEqual(r.buffered, 0)
    }

    func testZeroFillAndUnderrunCount() {
        let r = PlaybackRing(capacity: 64)
        r.expectingAudio = true
        _ = write(r, [5, 6])
        let (out, c) = read(r, 5)
        XCTAssertEqual(c, 2)
        XCTAssertEqual(out, [5, 6, 0, 0, 0])
        XCTAssertEqual(r.underruns, 1)
        r.expectingAudio = false
        _ = read(r, 5)
        XCTAssertEqual(r.underruns, 1)
    }

    func testFlushDropsBuffered() {
        let r = PlaybackRing(capacity: 8192)
        XCTAssertEqual(write(r, [Int16](repeating: 9, count: 4800)), 4800)
        _ = read(r, 1200)
        r.flush()
        XCTAssertEqual(r.buffered, 0)
        let (out, c) = read(r, 100)
        XCTAssertEqual(c, 0)
        XCTAssertEqual(out, [Int16](repeating: 0, count: 100))
        XCTAssertEqual(r.played, 1200)
    }

    func testPlayedExcludesZeroFill() {
        let r = PlaybackRing(capacity: 64)
        _ = write(r, [1, 2, 3])
        _ = read(r, 10)
        XCTAssertEqual(r.played, 3)
    }

    func testWrapAround() {
        let r = PlaybackRing(capacity: 8)
        for i in 0..<10 {
            let base = Int16(i * 5)
            let data = (0..<5).map { base + Int16($0) }
            XCTAssertEqual(write(r, data), 5)
            let (out, c) = read(r, 5)
            XCTAssertEqual(c, 5)
            XCTAssertEqual(out, data)
        }
        XCTAssertEqual(r.played, 50)
        // Over-capacity write is truncated.
        XCTAssertEqual(write(r, [Int16](repeating: 1, count: 20)), 8)
    }

    func testWriteAfterFlushAccepted() {
        let r = PlaybackRing(capacity: 8)
        _ = write(r, [Int16](repeating: 1, count: 8))
        r.flush()
        _ = read(r, 1) // consumer observes the flush
        XCTAssertEqual(write(r, [2, 3]), 2)
        let (out, c) = read(r, 2)
        XCTAssertEqual(c, 2)
        XCTAssertEqual(out, [2, 3])
    }

    func testLastRMS() {
        let r = PlaybackRing(capacity: 16)
        _ = write(r, [1000, -1000, 1000, -1000])
        _ = read(r, 4)
        XCTAssertEqual(r.lastRMS, 1000.0 / 32768.0, accuracy: 1e-4)
    }
}
