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
/// by word. The controller feeds it events and a timer (`tick`); the view draws it. It also
/// carries captions (#224): what Pixie says aloud, shown in the same place (voice mode: without
/// the input box).
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

    /// The newest typed message's job, kept when the ✕ or a caption puts it away: a late answer
    /// to it (a session's reply, #241) still shows.
    private var lastJob: String?
    /// Lines that came for a job before chat.send said which job is ours (newest per job).
    private var early: [String: ChatLine] = [:]
    private var pending = false
    /// Stop came while pending: the job is cancelled as soon as it is known.
    private var stopPending = false
    /// What is shown is a caption (Pixie's spoken words), not a typed message's answer.
    public private(set) var isCaption = false
    /// The caption's conversation is still going (it shows on its own until it ends).
    private var captionLive = false
    /// When what is shown was said: the card's "2m ago" (#224).
    public private(set) var saidAt: Date?

    public init() {}

    public var showsStop: Bool { phase == .thinking || (phase == .answering && !isCaption) }

    /// A live caption is up (voice mode draws the card for it alone).
    public var showsCaption: Bool { isCaption && captionLive && phase != .ready }

    /// There is something said to show again: a caption, or a typed message's finished answer.
    private var hasLast: Bool {
        saidAt != nil && !full.isEmpty && (phase == .done || (isCaption && phase == .answering))
    }

    /// The card without the input line: a live caption, or the last words on hover or always.
    public func showsCard(hovering: Bool, always: Bool) -> Bool {
        showsCaption || ((hovering || always) && hasLast)
    }

    /// How long ago it was said ("just now", "2m ago"), once it is all shown.
    public func ago(now: Date) -> String? {
        guard let saidAt, phase == .done else { return nil }
        let s = max(0, Int(now.timeIntervalSince(saidAt)))
        switch s {
        case ..<60: return "just now"
        case ..<3600: return "\(s / 60)m ago"
        case ..<86400: return "\(s / 3600)h ago"
        default: return "\(s / 86400)d ago"
        }
    }

    public var statusText: String? {
        if isCaption { return nil }
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
        isCaption = false
        captionLive = false
        saidAt = nil
        job = nil
        lastJob = nil
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
        lastJob = job
        isCaption = false
        captionLive = false
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
        isCaption = false
        captionLive = false
        saidAt = Date()
        job = nil
        pending = false
        stopPending = false
        early = [:]
        full = reason
        shown = reason
        phase = .done
    }

    public mutating func apply(_ line: ChatLine, now: Date = Date()) {
        if pending {
            early[line.job] = line
            return
        }
        if line.job == lastJob, line.status != "working",
           isCaption || line.job != job || phase == .ready {
            // #241: a late answer to the last message brings the card back with it (not after
            // Stop: the cancel's own line doesn't replace "Stopped.").
            job = line.job
            isCaption = false
            captionLive = false
            full = ""
            shown = ""
            phase = .done
        }
        guard !isCaption, line.job == job, phase == .thinking || phase == .answering || phase == .done else { return }
        if line.status == "working" {
            full = line.text
            shown = line.text
            phase = .thinking
        } else if !line.text.isEmpty && line.text == full && shown == full {
            // Only the end of what it already shows (a hand-off, #231): no typing it out again.
            phase = .done
            saidAt = now
        } else {
            full = line.text
            shown = ""
            phase = .answering
            saidAt = now
        }
    }

    /// The words can be put away (the ✕): a caption, or a finished answer; never while a typed
    /// message is at work (Stop is there then).
    public var canDismiss: Bool {
        isCaption ? phase != .ready : (phase == .done || phase == .stopped)
    }

    /// The ✕ (#224): the words go; hover and always show nothing until something new is said.
    public mutating func dismiss() {
        guard canDismiss else { return }
        isCaption = false
        captionLive = false
        saidAt = nil
        full = ""
        shown = ""
        phase = .ready
    }

    /// Puts the words away `secs` after they were said (0 keeps them), never while the
    /// conversation they came from goes on. True when it did.
    public mutating func dismissIfOlder(than secs: Int, now: Date) -> Bool {
        guard secs > 0, let saidAt, !(isCaption && captionLive), canDismiss,
              now.timeIntervalSince(saidAt) >= TimeInterval(secs) else { return false }
        dismiss()
        return true
    }

    /// What Pixie said aloud (the voice model's transcript). Only Pixie's side; a new caption
    /// replaces the last. In text mode it never covers a typed message still at work. True when
    /// it shows.
    @discardableResult
    public mutating func caption(role: String, text: String, now: Date = Date()) -> Bool {
        let words = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard role == "assistant", !words.isEmpty else { return false }
        if mode == "text" && !isCaption && (pending || phase == .thinking || phase == .answering) {
            return false
        }
        isCaption = true
        captionLive = true
        saidAt = now
        job = nil
        full = words
        shown = ""
        phase = .answering
        return true
    }

    /// The voice conversation ended: the caption stops showing on its own, all of it shown, and
    /// is kept for hover or always-show (#224).
    public mutating func conversationEnded() {
        guard isCaption else { return }
        captionLive = false
        if phase == .answering {
            shown = full
            phase = .done
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
