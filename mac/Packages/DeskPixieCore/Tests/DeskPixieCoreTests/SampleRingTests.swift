import XCTest
@testable import DeskPixieCore

final class SampleRingTests: XCTestCase {
    private func write(_ r: SampleRing, _ a: [Int16], ts: UInt64 = 0) -> Int {
        a.withUnsafeBufferPointer { r.write($0, timestamp: ts) }
    }
    private func read(_ r: SampleRing, _ n: Int) -> ([Int16], Int) {
        var out = [Int16](repeating: 7, count: n)
        let c = out.withUnsafeMutableBufferPointer { r.read(into: $0) }
        return (out, c)
    }

    func testWriteReadWrap() {
        let r = SampleRing(capacity: 8)
        for i in 0..<10 {
            let data = (0..<5).map { Int16(i * 5 + $0) }
            XCTAssertEqual(write(r, data), 5)
            XCTAssertEqual(r.available, 5)
            let (out, c) = read(r, 5)
            XCTAssertEqual(c, 5)
            XCTAssertEqual(out, data)
            XCTAssertEqual(r.available, 0)
        }
    }

    func testOverflowDropsNewest() {
        let r = SampleRing(capacity: 8)
        XCTAssertEqual(write(r, [1, 2, 3, 4, 5, 6]), 6)
        XCTAssertEqual(write(r, [7, 8, 9, 10]), 2)
        XCTAssertEqual(write(r, [11]), 0)
        let (out, c) = read(r, 8)
        XCTAssertEqual(c, 8)
        XCTAssertEqual(out, [1, 2, 3, 4, 5, 6, 7, 8])
    }

    func testReadDoesNotZeroFill() {
        let r = SampleRing(capacity: 8)
        _ = write(r, [1, 2])
        let (out, c) = read(r, 4)
        XCTAssertEqual(c, 2)
        XCTAssertEqual(out, [1, 2, 7, 7])
    }

    func testNewestTimestamp() {
        let r = SampleRing(capacity: 8)
        XCTAssertEqual(r.newestTimestamp, 0)
        _ = write(r, [1], ts: 100)
        _ = write(r, [2], ts: 250)
        XCTAssertEqual(r.newestTimestamp, 250)
    }
}
