import Foundation
import Observation

/// The voice preview calls (#480): the core makes, keeps and plays the sample; the window only
/// asks and shows how it goes (the `voice.preview` event).
public protocol VoicePreviewClient {
    /// `voice.preview {voice}`: the state it starts in ("loading" or "playing"); throws the
    /// reason it can't play (no key, Pixie talking), in plain words.
    func preview(_ voice: String) throws -> String
    /// `voice.previewStop`.
    func stop() throws
}

/// `VoicePreviewClient` over the Go core's `dp_call`.
public struct EngineVoicePreviewClient: VoicePreviewClient, Sendable {
    private let engine: CoreEngine

    public init(engine: CoreEngine) { self.engine = engine }

    private struct Args: Encodable { let voice: String }
    private struct Started: Decodable { let state: String }

    public func preview(_ voice: String) throws -> String {
        try engine.call("voice.preview", Args(voice: voice), as: Started.self).state
    }

    public func stop() throws { _ = try engine.call("voice.previewStop") }
}

/// The voice list's state: which voice is loading or playing, and why one couldn't play.
@MainActor
@Observable
public final class VoicePreviewModel {
    public enum RowState: Equatable, Sendable {
        case idle, loading, playing
        /// Why it couldn't play, in plain words.
        case failed(String)
    }

    /// The voices that aren't idle.
    public private(set) var states: [String: RowState] = [:]

    @ObservationIgnored private let client: VoicePreviewClient

    public init(client: VoicePreviewClient) { self.client = client }

    public func state(_ voice: String) -> RowState { states[voice] ?? .idle }

    /// The ▶/■ button: plays `voice`, or stops it when it is loading or playing. One voice at a
    /// time: playing another ends the one before.
    public func toggle(_ voice: String) {
        switch state(voice) {
        case .loading, .playing:
            stop()
        case .idle, .failed:
            states = [:]
            do {
                states[voice] = try client.preview(voice) == "playing" ? .playing : .loading
            } catch {
                states[voice] = .failed(Self.message(error))
            }
        }
    }

    public func stop() {
        try? client.stop()
        states = states.filter { if case .failed = $0.value { return true } else { return false } }
    }

    /// A `voice.preview` event from the core.
    public func apply(voice: String, state: String, error: String?) {
        switch state {
        case "loading": states[voice] = .loading
        case "playing": states[voice] = .playing
        case "error": states[voice] = .failed(error ?? "Couldn't play the preview.")
        default: states[voice] = nil // done
        }
    }

    /// The items whose name or description holds every word of `query` (any case), in order.
    public static func filter(_ items: [SettingsLayout.MenuItem], _ query: String) -> [Int] {
        let words = query.lowercased().split(whereSeparator: \.isWhitespace)
        return items.indices.filter { i in
            let text = (items[i].label + " " + (items[i].detail ?? "")).lowercased()
            return words.allSatisfy { text.contains($0) }
        }
    }

    private static func message(_ error: Error) -> String {
        if case DeskPixieEngineError.refused(let reason) = error { return reason }
        return "Couldn't play the preview."
    }
}
