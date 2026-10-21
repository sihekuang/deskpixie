import Foundation

/// One spoken line for a message typed in the text box, as the core's `chat.line` event sends
/// it (#221). `status`: working | question | done | failed.
public struct ChatLine: Decodable, Sendable, Equatable {
    public let job: String
    public let status: String
    public let text: String

    public init(job: String, status: String, text: String) {
        self.job = job
        self.status = status
        self.text = text
    }
}

/// The text box under Pixie (#221, spec docs/superpowers/specs/2026-10-21-text-mode-design.md
/// §4.3): one message at a time, and only the coordinator's spoken answer to it, typed out word
/// by word. The controller feeds it events and a timer (`tick`); the view draws it.
public struct QuickChatModel: Equatable, Sendable {
    public enum Phase: Equatable, Sendable { case ready, thinking, answering, done, stopped }

    public private(set) var phase: Phase = .ready
    /// The answer as far as it is shown; `full` is all of it.
    public private(set) var shown = ""
    public private(set) var full = ""
    /// The typed job the box is showing (nil before the first send).
    public private(set) var job: String?
    /// The box is open (text mode only); the controller sets it.
    public var open = false
    /// replyMode: "voice" or "text".
    public var mode = "voice"

    /// Lines that came for a job before chat.send said which job is ours (newest per job).
    private var early: [String: ChatLine] = [:]
    private var pending = false
    /// Stop came while pending: the job is cancelled as soon as it is known.
    private var stopPending = false

    public init() {}

    public var showsStop: Bool { phase == .thinking || phase == .answering }

    public var statusText: String? {
        switch phase {
        case .thinking: return "Thinking…"
        case .answering: return "Answering…"
        default: return nil
        }
    }

    public func canSend(_ draft: String) -> Bool {
        !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && !showsStop
    }

    /// The message is on its way (before chat.send answers): thinking, and lines that come
    /// meanwhile are kept until the job is known.
    public mutating func sending() {
        job = nil
        pending = true
        stopPending = false
        early = [:]
        phase = .thinking
        shown = ""
        full = ""
    }

    /// The message went: its job's lines are what the box shows now (any that came early too).
    /// True when Stop came first, so the core must cancel this job now.
    @discardableResult
    public mutating func sent(job: String) -> Bool {
        self.job = job
        if stopPending {
            stopPending = false
            pending = false
            early = [:]
            return true
        }
        let kept = pending ? early[job] : nil
        pending = false
        early = [:]
        phase = .thinking
        shown = ""
        full = ""
        if let kept { apply(kept) }
        return false
    }

    /// The message couldn't go: the reason is the answer.
    public mutating func failedToSend(_ reason: String) {
        job = nil
        pending = false
        stopPending = false
        early = [:]
        full = reason
        shown = reason
        phase = .done
    }

    public mutating func apply(_ line: ChatLine) {
        if pending {
            early[line.job] = line
            return
        }
        guard line.job == job, phase == .thinking || phase == .answering || phase == .done else { return }
        if line.status == "working" {
            full = line.text
            shown = line.text
            phase = .thinking
        } else {
            full = line.text
            shown = ""
            phase = .answering
        }
    }

    /// Shows the next word of the answer; false when nothing changed. The last word ends it.
    public mutating func tick() -> Bool {
        guard phase == .answering else { return false }
        let words = full.split(separator: " ", omittingEmptySubsequences: true)
        let have = shown.split(separator: " ", omittingEmptySubsequences: true).count
        guard have < words.count else {
            phase = .done
            return false
        }
        shown = words[0...have].joined(separator: " ")
        if have + 1 == words.count {
            shown = full
            phase = .done
        }
        return true
    }

    /// Stop: while thinking the work stops ("Stopped."); while typing out, the rest shows at once.
    /// True when the core must cancel the job (only while thinking: nothing runs while typing out).
    @discardableResult
    public mutating func stopped() -> Bool {
        switch phase {
        case .thinking:
            phase = .stopped
            if pending {
                // No job yet: sent(job:) says to cancel it.
                stopPending = true
                return false
            }
            return true
        case .answering:
            shown = full
            phase = .done
        default: break
        }
        return false
    }
}
