import Foundation
import Security

/// Where typed API keys are kept: read, save, delete by variable name (`OPENAI_API_KEY`).
/// Never log a value.
public protocol APIKeyStore {
    /// The stored key, or nil when there is none.
    func read(_ variable: String) throws -> String?
    /// Whether a key is stored, without reading it.
    func contains(_ variable: String) -> Bool
    /// Stores `value`, replacing any stored key.
    func save(_ variable: String, _ value: String) throws
    /// Removes the stored key; removing a missing one is not an error.
    func delete(_ variable: String) throws
}

public extension APIKeyStore {
    /// variable → key for each of `variables` that has a non-empty stored key (unreadable ones
    /// are left out), for `secrets.keychain`.
    func values(for variables: [String]) -> [String: String] {
        var out: [String: String] = [:]
        for v in variables {
            if let key = (try? read(v)) ?? nil, !key.isEmpty { out[v] = key }
        }
        return out
    }
}

/// A Keychain call failed with this `OSStatus`.
public struct KeychainError: Error, Equatable {
    public let status: OSStatus
}

/// API keys as generic passwords in the login Keychain: service "DeskPixie", account = the
/// variable name. Tests pass their own service name.
public struct KeychainSecrets: APIKeyStore, Sendable {
    public let service: String

    public init(service: String = "DeskPixie") {
        self.service = service
    }

    private func query(_ variable: String) -> [String: Any] {
        [kSecClass as String: kSecClassGenericPassword,
         kSecAttrService as String: service,
         kSecAttrAccount as String: variable]
    }

    public func read(_ variable: String) throws -> String? {
        var q = query(variable)
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else { throw KeychainError(status: status) }
        return String(decoding: data, as: UTF8.self)
    }

    public func contains(_ variable: String) -> Bool {
        var q = query(variable)
        q[kSecReturnAttributes as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        return SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess
    }

    public func save(_ variable: String, _ value: String) throws {
        let data = Data(value.utf8)
        let status = SecItemUpdate(query(variable) as CFDictionary, [kSecValueData as String: data] as CFDictionary)
        if status == errSecSuccess { return }
        guard status == errSecItemNotFound else { throw KeychainError(status: status) }
        var add = query(variable)
        add[kSecValueData as String] = data
        add[kSecAttrLabel as String] = "\(service) \(variable)"
        let added = SecItemAdd(add as CFDictionary, nil)
        guard added == errSecSuccess else { throw KeychainError(status: added) }
    }

    public func delete(_ variable: String) throws {
        let status = SecItemDelete(query(variable) as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else { throw KeychainError(status: status) }
    }
}
