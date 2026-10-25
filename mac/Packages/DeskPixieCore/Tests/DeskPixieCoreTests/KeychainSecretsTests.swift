import XCTest
@testable import DeskPixieCore

/// Round-trips against a throwaway Keychain service; every item made here is deleted.
final class KeychainSecretsTests: XCTestCase {
    private var store: KeychainSecrets!

    override func setUp() {
        store = KeychainSecrets(service: "DeskPixieTests-\(UUID().uuidString)")
    }

    override func tearDown() {
        for v in ["OPENAI_API_KEY", "GEMINI_API_KEY"] { try? store.delete(v) }
    }

    func test_service_defaults_to_DeskPixie() {
        XCTAssertEqual(KeychainSecrets().service, "DeskPixie")
    }

    func test_the_app_keeps_DeskPixie_and_a_copy_under_another_ID_its_own() {
        XCTAssertEqual(KeychainSecrets.forApp(bundleID: "com.sihekuang.deskpixie").service, "DeskPixie")
        XCTAssertEqual(KeychainSecrets.forApp(bundleID: nil).service, "DeskPixie")
        XCTAssertEqual(KeychainSecrets.forApp(bundleID: "com.sihekuang.deskpixie.uitest").service,
                       "DeskPixie (com.sihekuang.deskpixie.uitest)")
    }

    func test_save_read_replace_delete() throws {
        XCTAssertNil(try store.read("OPENAI_API_KEY"))
        XCTAssertFalse(store.contains("OPENAI_API_KEY"))
        try store.save("OPENAI_API_KEY", "sk-fake-one")
        XCTAssertEqual(try store.read("OPENAI_API_KEY"), "sk-fake-one")
        XCTAssertTrue(store.contains("OPENAI_API_KEY"))
        try store.save("OPENAI_API_KEY", "sk-fake-two")
        XCTAssertEqual(try store.read("OPENAI_API_KEY"), "sk-fake-two", "a second save replaces")
        XCTAssertEqual(store.values(for: ["OPENAI_API_KEY", "GEMINI_API_KEY"]), ["OPENAI_API_KEY": "sk-fake-two"])
        try store.delete("OPENAI_API_KEY")
        XCTAssertNil(try store.read("OPENAI_API_KEY"))
        XCTAssertNoThrow(try store.delete("OPENAI_API_KEY"), "deleting a missing item is fine")
    }
}
