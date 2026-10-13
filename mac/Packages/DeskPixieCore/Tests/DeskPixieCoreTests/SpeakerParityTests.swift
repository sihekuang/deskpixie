import XCTest
@testable import DeskPixieCore

/// Ports of ai-mascot `crates/audio-macos/src/counters.rs` (5) and `speaker.rs` (6) tests.
/// Rust splits the speaker into an rtrb ring + `Counters` + `render`; here `PlaybackRing` is
/// all three (write = `SpeakerWriter::push`, read = `render`, flush = `Counters::flush`,
/// `discardAll` = `discard_all`), so the cases run against it.
///
/// Omitted: speaker.rs `a_full_ring_warns_once_per_overflow`. Rust's writer drops what does
/// not fit and warns once per overflow; `AudioPump` never drops (it keeps the samples the ring
/// refuses and writes them on a later tick, `testSamplesTheRingRejectsAreKeptForTheNextTick`),
/// so there is no overflow to warn about.
final class SpeakerParityTests: XCTestCase {
    private func write(_ r: PlaybackRing, _ a: [Int16]) -> Int {
        a.withUnsafeBufferPointer { r.write($0) }
    }
    private func render(_ r: PlaybackRing, _ out: inout [Int16]) {
        _ = out.withUnsafeMutableBufferPointer { r.read(into: $0) }
    }
    private func read(_ r: PlaybackRing, _ n: Int) {
        var out = [Int16](repeating: 0, count: n)
        render(r, &out)
    }

    // MARK: counters.rs

    func test_starts_idle_with_nothing_played() {
        let r = PlaybackRing(capacity: 1000)
        XCTAssertTrue(r.isIdle)
        XCTAssertEqual(r.played, 0)
        XCTAssertEqual(r.buffered, 0)
    }

    func test_written_audio_is_busy_until_read() {
        let r = PlaybackRing(capacity: 1000)
        _ = write(r, [Int16](repeating: 1, count: 100))
        XCTAssertFalse(r.isIdle)
        read(r, 60)
        XCTAssertFalse(r.isIdle)
        read(r, 40)
        XCTAssertTrue(r.isIdle)
        XCTAssertEqual(r.played, 100)
    }

    func test_flush_discards_exactly_what_was_queued() {
        let r = PlaybackRing(capacity: 1000)
        _ = write(r, [Int16](repeating: 1, count: 100))
        read(r, 30)
        r.flush()
        XCTAssertEqual(r.buffered, 0, "the 70 queued samples are dropped")
        _ = write(r, [Int16](repeating: 2, count: 50)) // written after the flush: must still play
        XCTAssertFalse(r.isIdle)
        var out = [Int16](repeating: 0, count: 50)
        render(r, &out)
        XCTAssertEqual(out, [Int16](repeating: 2, count: 50))
        XCTAssertTrue(r.isIdle)
        XCTAssertEqual(r.played, 80, "discarded samples don't count as played")
    }

    func test_level_round_trips() {
        // Rust stores the render callback's level; here `read` sets it from what it played.
        let r = PlaybackRing(capacity: 100)
        _ = write(r, [8192, -8192, 8192, -8192])
        read(r, 4)
        XCTAssertEqual(r.lastRMS, 0.25)
    }

    func test_rms_of_i16() {
        XCTAssertEqual(PlaybackRing.rms([]), 0)
        XCTAssertEqual(PlaybackRing.rms([0, 0]), 0)
        XCTAssertEqual(PlaybackRing.rms([Int16.max, Int16.min]), 1, accuracy: 0.001)
    }

    // MARK: speaker.rs

    func test_a_frame_that_fits_is_queued_whole() {
        let r = PlaybackRing(capacity: 10)
        XCTAssertEqual(write(r, [Int16](repeating: 1, count: 6)), 6)
        XCTAssertFalse(r.isIdle)
    }

    func test_render_plays_what_is_queued_and_pads_with_silence() {
        let r = PlaybackRing(capacity: 10)
        _ = write(r, [1000, 2000, 3000])
        var out = [Int16](repeating: 7, count: 5)
        render(r, &out)
        XCTAssertEqual(out, [1000, 2000, 3000, 0, 0])
        XCTAssertTrue(r.isIdle)
        XCTAssertEqual(r.played, 3)
        XCTAssertGreaterThan(r.lastRMS, 0)
    }

    func test_render_skips_flushed_audio() {
        let r = PlaybackRing(capacity: 10)
        _ = write(r, [1, 1, 1, 1])
        r.flush()
        _ = write(r, [5, 6])
        var out = [Int16](repeating: 0, count: 4)
        render(r, &out)
        XCTAssertEqual(out, [5, 6, 0, 0])
        XCTAssertEqual(r.played, 2)
    }

    func test_discarding_a_stranded_queue_makes_playback_idle() {
        let r = PlaybackRing(capacity: 10)
        _ = write(r, [Int16](repeating: 1, count: 7))
        XCTAssertFalse(r.isIdle, "no unit: this audio would wait forever")
        XCTAssertEqual(r.discardAll(), 7)
        XCTAssertTrue(r.isIdle)
        XCTAssertEqual(r.played, 0, "discarded audio was never heard")
        XCTAssertEqual(r.lastRMS, 0)
        XCTAssertEqual(r.discardAll(), 0)
        // The discard is a read: the ring's full capacity is writable again.
        XCTAssertEqual(write(r, [Int16](repeating: 1, count: 10)), 10)
    }

    func test_audio_queued_after_a_discard_still_plays() {
        let r = PlaybackRing(capacity: 10)
        _ = write(r, [1, 1, 1])
        r.flush()
        r.discardAll()
        _ = write(r, [9, 9])
        var out = [Int16](repeating: 0, count: 2)
        render(r, &out)
        XCTAssertEqual(out, [9, 9])
    }
}
