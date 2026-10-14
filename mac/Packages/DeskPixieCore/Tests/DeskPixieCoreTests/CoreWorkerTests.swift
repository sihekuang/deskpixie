import DeskPixieCore
import Foundation
import XCTest

final class CoreWorkerTests: XCTestCase {
    func testDrainWaitsForQueuedWork() {
        let worker = CoreWorker(label: "test.drain")
        let log = Log<Int>()
        let gate = DispatchSemaphore(value: 0)
        worker.async { gate.wait(); log.add(1) }
        worker.async { log.add(2) }
        // Release the first item shortly after drain starts waiting.
        DispatchQueue.global().asyncAfter(deadline: .now() + 0.1) { gate.signal() }
        worker.drain()
        XCTAssertEqual(log.values, [1, 2])
    }

    func testDrainOnWorkerQueueDoesNotDeadlock() {
        let worker = CoreWorker(label: "test.reentrant")
        let done = expectation(description: "drained on worker")
        worker.async {
            XCTAssertTrue(worker.isOnWorker)
            worker.drain()
            done.fulfill()
        }
        wait(for: [done], timeout: 2)
        XCTAssertFalse(worker.isOnWorker)
    }

    func testWorkAfterCloseIsSkipped() async {
        let worker = CoreWorker(label: "test.close")
        let log = Log<Int>()
        let gate = DispatchSemaphore(value: 0)
        worker.async { gate.wait(); log.add(1) }
        worker.close()
        XCTAssertTrue(worker.isClosed)
        worker.async { log.add(2) }
        let ran: Bool? = await worker.run { log.add(3); return true }
        XCTAssertNil(ran)
        gate.signal()
        worker.drain()
        // Accepted before close: runs. Submitted after: never runs.
        XCTAssertEqual(log.values, [1])
    }

    func testShutdownDrainsThenRejects() async {
        let worker = CoreWorker(label: "test.shutdown")
        let log = Log<Int>()
        worker.async { Thread.sleep(forTimeInterval: 0.05); log.add(1) }
        worker.shutdown()
        XCTAssertEqual(log.values, [1])
        let ran: Void? = await worker.run { log.add(2) }
        XCTAssertNil(ran)
        XCTAssertEqual(log.values, [1])
    }

    func testGeometrySavesStayInOrder() {
        let worker = CoreWorker(label: "test.geometry")
        let saved = Log<CanvasGeometry>()
        let saver = CanvasGeometrySaver(worker: worker) { g in
            // The first save is slow: a later save must still land after it.
            if g.x == 1 { Thread.sleep(forTimeInterval: 0.1) }
            saved.add(g)
        }
        let g1 = CanvasGeometry(x: 1, y: 0, width: 400, height: 300)
        let g2 = CanvasGeometry(x: 2, y: 0, width: 400, height: 300)
        let g3 = CanvasGeometry(x: 3, y: 0, width: 400, height: 300)
        saver.saveSettled(g1)
        saver.saveSettled(g2)
        // Close / quit: drains the settled saves, then saves on this thread.
        saver.saveNow(g3)
        XCTAssertEqual(saved.values, [g1, g2, g3])
    }
}

private final class Log<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var _values: [T] = []
    var values: [T] { lock.withLock { _values } }
    func add(_ v: T) { lock.withLock { _values.append(v) } }
}
