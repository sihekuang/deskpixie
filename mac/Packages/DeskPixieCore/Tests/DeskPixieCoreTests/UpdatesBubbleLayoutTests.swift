import XCTest
@testable import DeskPixieCore

final class UpdatesBubbleLayoutTests: XCTestCase {
    private func line(_ text: String, kind: String = "session", title: String? = nil) -> UpdateLine {
        UpdateLine(id: 1, text: text, kind: kind, at: 0, job: nil, worker: nil, title: title)
    }

    func testShortLinesKeepTheNarrowBubble() {
        XCTAssertEqual(UpdatesBubbleLayout.width(for: [line("Tests pass.")]), UpdatesBubbleLayout.minWidth)
        XCTAssertEqual(UpdatesBubbleLayout.width(for: []), UpdatesBubbleLayout.minWidth)
    }

    // #278: the bubble is as wide as its line needs, up to the most it gets; past that the line
    // wraps to three lines, so a session's gist (up to 120 characters) isn't cut after two.
    func testALongLineWidensTheBubbleWithItsContent() {
        let gist = "Pushed a1b2c3d, see core/pix/questions.go:152 and docs/ or https://github.com/sihekuang/deskpixie/pull/273, ask dan@…"
        let w = UpdatesBubbleLayout.width(for: [line(gist)])
        XCTAssertEqual(w, UpdatesBubbleLayout.maxWidth)
        let medium = UpdatesBubbleLayout.width(for: [line("Pushed a1b2c3d, see core/pix/questions.go:152")])
        XCTAssertGreaterThan(medium, UpdatesBubbleLayout.minWidth)
        XCTAssertLessThan(medium, UpdatesBubbleLayout.maxWidth)
        XCTAssertEqual(UpdatesBubbleLayout.maxLines, 3)
    }

    func testAQuestionStillWidensToTheMaximum() {
        let long = String(repeating: "Which database should the service use for its cache? ", count: 3)
        XCTAssertEqual(UpdatesBubbleLayout.width(for: [line(long, kind: "question"), line("ok")]),
                       UpdatesBubbleLayout.maxWidth)
    }

    func testTheWidestLineWins() {
        let short = UpdatesBubbleLayout.width(for: [line("Pushed a1b2c3d, see the docs")])
        let both = UpdatesBubbleLayout.width(for: [line("Pushed a1b2c3d, see the docs"),
                                                   line("Pushed a1b2c3d, see core/pix/questions.go:152")])
        XCTAssertGreaterThan(both, short)
    }
}
