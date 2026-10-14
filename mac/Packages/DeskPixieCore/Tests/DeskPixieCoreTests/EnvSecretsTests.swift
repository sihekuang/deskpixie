import Foundation
import XCTest
@testable import DeskPixieCore

final class EnvSecretsTests: XCTestCase {
    private var dir: URL!

    override func setUpWithError() throws {
        dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("EnvSecretsTests-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: dir)
    }

    private func dotenv(_ text: String) throws -> URL {
        let url = dir.appendingPathComponent(".env")
        try text.write(to: url, atomically: true, encoding: .utf8)
        return url
    }

    func testEnvironmentWinsOverDotenv() throws {
        let url = try dotenv("OPENAI_API_KEY=from-file\n")
        XCTAssertEqual(EnvSecrets.openAIKey(env: ["OPENAI_API_KEY": "from-env"], dotenvURL: url), "from-env")
    }

    func testEmptyEnvironmentValueFallsBackToDotenv() throws {
        let url = try dotenv("OPENAI_API_KEY=from-file\n")
        XCTAssertEqual(EnvSecrets.openAIKey(env: ["OPENAI_API_KEY": ""], dotenvURL: url), "from-file")
    }

    func testDotenvIsParsed() throws {
        let url = try dotenv("""
        # comment line
        OTHER=1

          OPENAI_API_KEY = sk-test-123
        LATER=2
        """)
        XCTAssertEqual(EnvSecrets.openAIKey(env: [:], dotenvURL: url), "sk-test-123")
    }

    func testExportPrefixAndQuotesAreStripped() throws {
        XCTAssertEqual(EnvSecrets.openAIKey(env: [:], dotenvURL: try dotenv("export OPENAI_API_KEY=\"sk-dq\"\n")), "sk-dq")
        XCTAssertEqual(EnvSecrets.openAIKey(env: [:], dotenvURL: try dotenv("OPENAI_API_KEY='sk-sq'\n")), "sk-sq")
    }

    func testCommentedOutKeyIsIgnored() throws {
        let url = try dotenv("# OPENAI_API_KEY=old\n")
        XCTAssertNil(EnvSecrets.openAIKey(env: [:], dotenvURL: url))
    }

    func testMissingFileAndVariableIsNil() {
        let url = dir.appendingPathComponent("does-not-exist.env")
        XCTAssertNil(EnvSecrets.openAIKey(env: [:], dotenvURL: url))
    }

    func testEmptyValueInDotenvIsNil() throws {
        XCTAssertNil(EnvSecrets.openAIKey(env: [:], dotenvURL: try dotenv("OPENAI_API_KEY=\n")))
        XCTAssertNil(EnvSecrets.openAIKey(env: [:], dotenvURL: try dotenv("OPENAI_API_KEY=\"\"\n")))
    }

    func testCRLFLineEndings() throws {
        let url = try dotenv("A=1\r\nOPENAI_API_KEY=sk-crlf\r\n")
        XCTAssertEqual(EnvSecrets.openAIKey(env: [:], dotenvURL: url), "sk-crlf")
    }
}
