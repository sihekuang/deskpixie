import XCTest
@testable import DeskPixieCore

/// `dp_call` plumbing. `LibDeskPixieEngine` runs against CDPStubs here: "stub.echo" answers
/// `{"ok":<args>}`, "stub.fail" an error, anything else "unknown method <m>".
final class CallTests: XCTestCase {
    private struct Args: Codable, Equatable { var id: String; var n: Int }

    func testCallReturnsOkPayload() throws {
        let out = try LibDeskPixieEngine().call("stub.echo", Args(id: "talk", n: 3), as: Args.self)
        XCTAssertEqual(out, Args(id: "talk", n: 3))
    }

    func testCallWithoutArgsSendsEmptyObject() throws {
        let data = try LibDeskPixieEngine().call("stub.echo")
        XCTAssertEqual(String(decoding: data, as: UTF8.self), "{}")
    }

    func testCallErrorThrowsCoreMessage() {
        XCTAssertThrowsError(try LibDeskPixieEngine().call("stub.fail")) {
            XCTAssertEqual($0 as? DeskPixieEngineError, .callFailed("stub failure"))
        }
    }

    func testUnknownMethodThrows() {
        XCTAssertThrowsError(try LibDeskPixieEngine().call("app.nope")) {
            XCTAssertEqual($0 as? DeskPixieEngineError, .callFailed("unknown method app.nope"))
        }
    }

    func testEnvelopeUnwrap() throws {
        XCTAssertEqual(String(decoding: try CallEnvelope.unwrap(Data(#"{"ok":{"b":1,"a":[true]}}"#.utf8)), as: UTF8.self),
                       #"{"a":[true],"b":1}"#)
        XCTAssertEqual(String(decoding: try CallEnvelope.unwrap(Data(#"{"ok":"x"}"#.utf8)), as: UTF8.self), #""x""#)
        XCTAssertThrowsError(try CallEnvelope.unwrap(Data(#"{"error":"nope"}"#.utf8))) {
            XCTAssertEqual($0 as? DeskPixieEngineError, .callFailed("nope"))
        }
        XCTAssertThrowsError(try CallEnvelope.unwrap(Data(#"{"error":"Pick @w1 from the list.","code":"refused"}"#.utf8))) {
            XCTAssertEqual($0 as? DeskPixieEngineError, .refused("Pick @w1 from the list."))
        }
        XCTAssertThrowsError(try CallEnvelope.unwrap(Data(#"{"error":"nope","code":"other"}"#.utf8))) {
            XCTAssertEqual($0 as? DeskPixieEngineError, .callFailed("nope"), "only a refusal is one")
        }
        XCTAssertThrowsError(try CallEnvelope.unwrap(Data("garbage".utf8)))
        XCTAssertThrowsError(try CallEnvelope.unwrap(Data("{}".utf8)))
    }
}
