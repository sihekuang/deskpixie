import Foundation

/// Any JSON value: the settings snapshot's `values` tree and what `settings.set` takes.
/// Whole numbers decode as `.int`, so `8` and `8.0` from Go compare as the same menu value.
public enum JSONValue: Sendable, Equatable, Codable, CustomStringConvertible {
    case null
    case bool(Bool)
    case int(Int64)
    case double(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() {
            self = .null
        } else if let b = try? c.decode(Bool.self) {
            self = .bool(b)
        } else if let i = try? c.decode(Int64.self) {
            self = .int(i)
        } else if let d = try? c.decode(Double.self) {
            if d.rounded() == d, let i = Int64(exactly: d) {
                self = .int(i)
            } else {
                self = .double(d)
            }
        } else if let s = try? c.decode(String.self) {
            self = .string(s)
        } else if let a = try? c.decode([JSONValue].self) {
            self = .array(a)
        } else {
            self = .object(try c.decode([String: JSONValue].self))
        }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .int(let i): try c.encode(i)
        case .double(let d): try c.encode(d)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    public var boolValue: Bool? {
        if case .bool(let b) = self { return b }
        return nil
    }

    public var intValue: Int64? {
        if case .int(let i) = self { return i }
        return nil
    }

    public var stringValue: String? {
        if case .string(let s) = self { return s }
        return nil
    }

    public var arrayValue: [JSONValue]? {
        if case .array(let a) = self { return a }
        return nil
    }

    /// The value at a dotted path (`agent.workers.max`), or nil when any step is missing.
    public func at(_ path: String) -> JSONValue? {
        var v: JSONValue? = self
        for key in path.split(separator: ".", omittingEmptySubsequences: false) {
            guard case .object(let o)? = v else { return nil }
            v = o[String(key)]
        }
        return v
    }

    /// JSON text, as Rust's `Value::to_string` writes it.
    public var description: String {
        switch self {
        case .null: return "null"
        case .bool(let b): return b ? "true" : "false"
        case .int(let i): return String(i)
        case .double(let d): return String(d)
        case .string(let s):
            let data = (try? JSONEncoder().encode(s)) ?? Data()
            return String(decoding: data, as: UTF8.self)
        case .array(let a): return "[" + a.map(\.description).joined(separator: ",") + "]"
        case .object(let o):
            return "{" + o.keys.sorted().map { JSONValue.string($0).description + ":" + o[$0]!.description }
                .joined(separator: ",") + "}"
        }
    }
}

extension JSONValue: ExpressibleByNilLiteral, ExpressibleByBooleanLiteral, ExpressibleByIntegerLiteral,
    ExpressibleByStringLiteral, ExpressibleByArrayLiteral, ExpressibleByDictionaryLiteral {
    public init(nilLiteral: ()) { self = .null }
    public init(booleanLiteral value: Bool) { self = .bool(value) }
    public init(integerLiteral value: Int64) { self = .int(value) }
    public init(stringLiteral value: String) { self = .string(value) }
    public init(arrayLiteral elements: JSONValue...) { self = .array(elements) }
    public init(dictionaryLiteral elements: (String, JSONValue)...) {
        self = .object(Dictionary(elements, uniquingKeysWith: { _, last in last }))
    }
}
