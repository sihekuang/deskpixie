# DeskPixie: OpenAI GPT-Live as a third voice provider

Date: 2026-10-13
Status: **Draft for review.** Spec only; no code. The open questions in §12 need the user's answers before a plan is written.
Builds on: `docs/specs/2026-10-13-deskpixie-port-design.md` (the Go core layout, §4) and the existing adapters in `core/voice/openai` (Realtime) and `core/voice/gemini` (Gemini Live).

## 1. Goal

Bit can talk through OpenAI **GPT-Live** (`gpt-live-1`) as well as OpenAI Realtime and Gemini Live. The user picks it in Settings ▸ Voice. Everything else stays the same:
- the local tools (`get_time`, `get_weather`, `calculate`, `timer`, `media`, `end_conversation`)
- delegation to the Claude coordinator (`delegate`, `jobs`, `answer_job`)
- the agent's spoken notices ("Agent reply:", "Agent question:", "Agent progress:" …)
- the mouth, the mascot states, the chimes, the recordings and the status lines

### Success criteria
- A new adapter package `core/voice/openailive` implements `voice.Model` / `voice.Session`. Nothing outside `core/voice/openailive` and the composition root (`core/app/voice.go`, `core/settings/schema.go`, `core/voice/secrets.go`) names GPT-Live.
- The adapter passes the shared conformance suite (`voicetest.Run`) against a scripted fake GPT-Live server, plus its own protocol table tests.
- A `-tags live` smoke shows a real turn, a tool call and a close against the real service.
- A key without GPT-Live access gives a clear status line, not "Voice: connection lost".

### Non-goals
- WebRTC, SIP, sideband (`/attach`) connections, forking and recording download. DeskPixie is a server-side WebSocket client; the Go core owns the key.
- Automatic reconnect or session fork after a drop (§9.3; an open question).
- Changing how the other two providers behave. Any state-machine change is gated so Realtime and Gemini keep passing `runtime_parity_test.go` unchanged.
- Hosted Responses tools (web search, code interpreter …). Bit's research goes to the Claude coordinator through `delegate` (§12 Q5).
- Mid-session voice, instruction or model changes (GPT-Live forbids them anyway).

## 2. Protocol summary (as of 2026-10-13)

Sources, with official OpenAI pages taking precedence:
- [OA-live] https://developers.openai.com/api/docs/guides/live
- [OA-conv] https://developers.openai.com/api/docs/guides/live-conversations
- [OA-deleg] https://developers.openai.com/api/docs/guides/live-delegation
- [OA-prompt] https://developers.openai.com/api/docs/guides/live-prompting
- [OA-ws] https://developers.openai.com/api/reference/resources/live/primary-websocket
- [OA-fork] https://developers.openai.com/api/reference/resources/live/fork-websocket
- [OA-migrate] https://developers.openai.com/api/docs/guides/live-migration
- [OA-cost] https://developers.openai.com/api/docs/guides/voice-latency-cost and https://developers.openai.com/api/docs/models/gpt-live-1
- [MS] https://learn.microsoft.com/en-us/azure/foundry/openai/gpt-live-reference (Azure Foundry event reference; dated 2026-09-04)
- [LK] https://docs.livekit.io/agents/models/realtime/plugins/gpt-live/
- [PAI] https://pydantic.dev/docs/ai/api/realtime/openai_live/

### 2.1 Connection and auth
| Fact | Value | Source |
|---|---|---|
| Endpoint | `wss://api.openai.com/v1/live/sessions`, no query parameters (the model goes in `session.start`, not the URL) | OA-ws |
| Auth | `Authorization: Bearer <OPENAI_API_KEY>`. No `OpenAI-Beta` header is documented. No ephemeral client secrets exist. | OA-ws, PAI |
| Handshake | Send `session.start` right after connecting; wait for `session.started` before sending audio or anything else | OA-ws, MS |
| Azure | `/openai/v1/live/sessions` on a Foundry resource. Out of scope. | MS |

### 2.2 Session config (`session.start`)
The config object is **strict: unknown fields are rejected** [MS].

```json
{ "type": "session.start", "event_id": "start_1",
  "session": {
    "model": "gpt-live-1",
    "instructions": "…spoken behaviour…",
    "audio": { "format": { "type": "audio/pcm", "rate": 24000 },
               "output": { "voice": "marin" } },
    "delegation": { "type": "responses",
      "responses": { "model": "<backend model>", "instructions": "…tool rules…",
                     "tools": [ { "type": "function", "name": "get_weather", "description": "…",
                                  "parameters": { "type": "object", "properties": {} } } ],
                     "tool_choice": "auto", "parallel_tool_calls": true } },
    "store": false } }
```

| Field | Notes | Source |
|---|---|---|
| `model` | Required. `gpt-live-1` is the only listed model and snapshot. **There is no mini.** | OA-ws, OA-cost |
| `instructions` | Up to 16,384 tokens. **Immutable**; only `session.instructions.append` adds to it. | OA-conv, MS |
| `input` | Optional seed history: up to 128 text messages and 8,192 tokens (roles developer/user/assistant) | OA-conv |
| `audio.format` | WebSocket only: `audio/pcm` at 16000 or 24000 Hz (mono PCM16 LE), or `audio/pcmu` / `audio/pcma` at 8000 Hz. Default PCM16 24 kHz. Applies to input and output. | OA-ws, MS |
| `audio.output.voice` | A built-in name, or a custom voice `{"id": "voice_…"}`. Default `marin`. Immutable. | OA-ws, LK |
| `delegation` | `{"type":"client"}` or `{"type":"responses","responses":{…}}`; omitted/`null` = client. Type immutable. | OA-deleg |
| `delegation.responses` | `model` (required), `instructions`, `tools`, `tool_choice` (`auto`/`required`/`none`/named), `parallel_tool_calls`, `max_output_tokens` (≥ 16), `service_tier` (`auto`/`default`/`flex`/`priority`), `reasoning`, `text` | OA-deleg, MS |
| `store` | Default `false`. `true` keeps a 30-day stored recording that enables forking. | OA-conv |

`session.update` can change **only** `delegation.responses` (replaced as a whole object) [MS].

**Built-in voices** [OA-ws]: alloy, ash, ballad, beacon, bossa, brise, cedar, cinder, coral, delta, echo, flitz, gleam, harema, juni, marin, meridian, nira, noeul, nuri, quartz, ripple, sage, shida, shimmer, sillage, stone, tempo, verse, vesper, willow. OA-conv describes 12 of them with accents (e.g. `vesper` British masculine, `willow` Irish feminine, `gleam`/`meridian` North American, `bossa`/`tempo` Brazilian Portuguese). Custom voices: `{"id": "voice_…"}`.

### 2.3 Events
**Client → server** [OA-ws, MS]: `session.start`, `session.update`, `session.input_audio.append` (`audio`: base64 PCM, even byte count, not acknowledged), `session.input_audio.mute` / `.unmute`, `session.instructions.append`, `session.thinking.append`, `session.commentary.append`, `response.item.create`, `response.create`, `session.close`.

**Server → client** [MS, OA-conv]:

| Event | Payload | Note |
|---|---|---|
| `session.started` | `session` {`id`, `expires_at`, `model`, `audio`, `delegation`, …} | Ready |
| `session.updated` | full session | ack of `session.update` |
| `session.output_audio.delta` | `delta` (base64 PCM16 24 kHz mono), `start_ms`, `end_ms` | **No output-audio-done event.** [MS] says a gap between ranges is omitted silence; [PAI] observed a continuous track of 100 ms frames, silent while the model is quiet. The adapter must handle both. |
| `session.input_transcript.delta` / `session.output_transcript.delta` | `delta`, `start_ms`, `end_ms` | Fragments with **no item id and no turn-complete event**; boundaries follow audio cadence, user and assistant fragments interleave. Input transcription is always on. |
| `session.instructions.appended` / `.thinking.appended` / `.commentary.appended` | `client_event_id` | Context accepted (when the timeline reaches it), not "spoken" |
| `session.delegation.created` | `delegation` {`id`, `type`, `target`: `client`/`responses`}, `response_id` (responses), `offset_ms` | GPT-Live decided to delegate |
| `response.event` | `delegation_id`, nested `event` (a Responses streaming event) | Dispatch on `event.type` |
| `session.usage.updated` | `usage.seconds` (cumulative), `context_window.usage_ratio` | About once a minute |
| `session.closed` | `reason` (`close_requested`, `expired`, `content`, `remote_hangup`, `connection_lost`), final `usage` | Keep reading until it arrives |
| `error` | `error` {`type`, `code`, `message`, `param`, `client_event_id`} | A command error does not necessarily close the session; a startup error prevents `session.started` |

### 2.4 Turn-taking and barge-in
- **Full duplex.** The caller and the assistant can speak at the same time; GPT-Live decides when to speak, when to backchannel ("mm-hm") and when to stop [OA-conv, OA-live].
- **No VAD / turn-detection settings, no manual commit, no `response.create` for voice turns, no truncate.** [OA-migrate] maps `conversation.item.truncate` to nothing: interruption behaviour comes from the prompt's "Interruption policy", and corrective instructions can't retract audio the user already heard. [LK]: "Truncation isn't supported. The model's context keeps the full turn, even if the user heard only part of it." [PAI]: "There is no manual turn control, interruption, truncation, or turn-detection setting."
- **No per-response end event.** Clients synthesize turn boundaries: [PAI] closes a turn after `2000 ms` of output silence once no delegated work is outstanding; [LK] uses an adaptive noise gate (pauses under ~0.5 s don't split a turn).
- `session.instructions.append` mid-speech can interrupt speech in progress. Moderation can cut audio for the rest of the current speech and send an `error` without closing [OA-conv].
- The session timeline only advances while audio flows [PAI]; an app with no mic audio must stream silence.

### 2.5 Delegation
Two modes, fixed per session [OA-deleg]:

**Responses delegation.** GPT-Live calls a configured Responses model with the conversation context. Flow for a function tool:
1. `session.delegation.created` (`target: "responses"`, `response_id`).
2. `response.event` envelopes; completed calls are in nested `response.output_item.done` with `item.type: "function_call"`, `call_id`, `name`, `arguments`. (The forwarded `response.completed` has an empty `output`; don't read calls from it.)
3. The app runs the tool and sends `response.item.create` {`item`: {`type`: `function_call_output`, `call_id`, `output`: "<JSON string>"}} for **every** pending call (missing ones → `function_call_outputs_required`).
4. Then `response.create` to continue: "Appending results alone does not resume the backend."
5. The backend's answer goes back into the live conversation and GPT-Live speaks it. The voice keeps talking while delegated work runs ("delegated work doesn't block the conversation" [LK]).

Function tools are declared like Responses tools: `{"type":"function","name","description","parameters"}` [MS example]. Tools may be replaced mid-session with `session.update` (whole `delegation.responses`). Backend token usage is billed and reported separately (nested `response.completed`).

Backend model names differ between sources: OA-deleg suggests `gpt-6-luna` (start) or `gpt-6-sol` (harder tasks); LK defaults to `gpt-5.6-luna`; PAI's `AUTO_BACKEND_MODEL = 'gpt-6-sol'`; MS's example uses `gpt-5.5`. The adapter takes it from settings (§8).

**Client delegation.** `session.delegation.created` with `target: "client"` and a `delegation.id`, **but no task text**: the app has to work out the task from the transcripts and its own state, then answer with `session.commentary.append` (to be spoken, paraphrased) or `session.thinking.append` (quiet context) carrying that `delegation_id`. No typed function calls exist in this mode [PAI, LK].

### 2.6 Context appends
`session.commentary.append` (the model should say it, paraphrased), `session.thinking.append` (facts and progress, not spoken on arrival) and `session.instructions.append` (trusted instructions; can interrupt current speech). Each takes `event_id`, `delegation_id` (`null` = session-wide; non-null must be a known client delegation) and `content`: a plain string of **at most 500 tokens** [OA-conv, OA-deleg].

### 2.7 Limits, close and recovery
- Context window 128k tokens, summarised in the background; above 90% a replacement voice engine starts inside the same session [OA-conv].
- There is a duration limit (close reason `expired`, `session.expires_at`), but its value is not published [OA-conv].
- `session.close` cancels queued backend responses; keep reading until `session.closed` for final usage [OA-conv].
- No automatic reconnect. Recovery: fork a stored session (`store: true`, `wss://…/v1/live/sessions/{id}/fork`, send `session.start` with only `audio.format` / `delegation.responses` / `store` overrides), or start a new session seeded with `input` text history [OA-conv, OA-fork]. [PAI]: neither restores the reply or delegated work in flight.

### 2.8 Pricing and access
- **$0.05 per minute** of session time, billed per second with no rounding up; **silence and idle time are billed** (mute does not stop billing). Backend Responses tokens and tool charges are billed separately [OA-cost].
- Concurrent-session limits: Build 50, Launch 300, Grow 500. **Free tier unsupported** [OA-cost].
- Access: OpenAI's pages don't call it alpha. **LiveKit says it "requires an OpenAI API key on an account with GPT-Live alpha access."** Treat access as not guaranteed and check it first (§10).

## 3. How GPT-Live maps onto `voice.Model`

The existing interface (`core/voice/types.go`) is kept. The adapter turns GPT-Live's continuous, duplex stream into the same per-turn event shape the `Conversation` state machine already gets from Realtime and Gemini, following the precedent `core/voice/gemini/protocol.go` already sets (derived `ResponseCreated` / `SpeechStarted` / `SpeechStopped`, local cancel).

| `voice` concept | Realtime | GPT-Live mapping | Fit |
|---|---|---|---|
| `Model.Name()` | `openai-realtime` | `openai-live` | ✓ |
| `Connect` → `EvReady` | `session.update` → `session.updated` | dial, `session.start` → `session.started` | ✓ |
| `SessionConfig.Model/Voice` | URL query / session | `session.model` / `audio.output.voice` (`voice_…` ids sent as `{"id": …}`) | ✓ |
| `SessionConfig.Instructions` | one prompt | split in two: the **Live** prompt (spoken behaviour) and the **backend** prompt (tool rules) (§3.4) | adapter |
| `SessionConfig.Tools` | session tools | `delegation.responses.tools` | ✓ |
| `TurnDetection`, `NoisyRoom`, `ProviderNoiseReduction` | VAD settings | **No equivalent; ignored.** Noisy room becomes a sentence in the Live prompt's interruption policy (§12 Q6). | ✗ |
| `CmdAudio` | `input_audio_buffer.append` | `session.input_audio.append` (24 kHz PCM16, same as `audio.SampleRate`, no resampling) | ✓ |
| `EvAudioDelta` | `response.output_audio.delta` | `session.output_audio.delta`, **only inside an assistant span** (§3.1) | derived |
| `EvSpeechStarted/Stopped` | server VAD | **synthesized** from input transcript fragments (§3.1) | derived |
| `EvResponseCreated/Done` | `response.created/done` | **synthesized** assistant span (§3.1) | derived |
| `EvTranscript` | `.done` events | joined fragments, emitted per span (§3.1) | derived |
| `EvToolCall` / `CmdToolResult` | function call in `response.done` / `function_call_output` + `response.create` | nested `response.output_item.done` / `response.item.create` + `response.create` (§3.3) | ✓ |
| `CmdCancelResponse{HeardMs}` | `response.cancel` + `conversation.item.truncate` | **no wire message**: local close + mute until GPT-Live goes quiet (§3.2) | ✗ |
| `CmdInject{Text, Respond}` | user message + `response.create` | `session.commentary.append` (Respond) / `session.thinking.append` (not) (§3.5) | ✓ |
| `CmdClose` / `Close()` | WS close | `session.close`, wait ≤ 1 s for `session.closed`, then WS close | ✓ |

### 3.1 Synthesized turn boundaries (the `turnClock`)
GPT-Live has no speech-started/stopped, no response created/done and no transcript-done. The adapter derives them in a pure, clock-injected component, `turnClock`, polled on a timer like the Gemini codec's `poll`. It tracks two independent spans:

**Assistant span.**
- Opens on the first `session.output_audio.delta` whose RMS is above `outputSpeechRMS` (or any delta when the server omits silence). Emits `EvResponseCreated` (preceded by `EvSpeechStopped` if a user span is still open), then `EvAudioDelta`.
- While open, **every** output delta is forwarded, including silent frames, so pauses inside a reply keep their length. Gaps between `start_ms`/`end_ms` ranges inside a span are filled with silence (bounded to `assistantQuiet`), so playback timing matches the server timeline.
- Closes after `assistantQuiet` of silent or missing output **and no delegation outstanding** (a `session.delegation.created` whose backend response has not completed or is waiting for our tool result). Emits the joined assistant transcript (`EvTranscript{assistant}`), then `EvResponseDone`. Waiting for delegations keeps "tool calls arrive before their response's ResponseDone", which the conformance suite and `Conversation` rely on.
- Outside a span, silent output frames are dropped (no `EvAudioDelta`), so an idle GPT-Live never looks like "Bit is talking".

**User span.**
- Opens on the first `session.input_transcript.delta` with non-blank text while closed. Emits `EvSpeechStarted`, **unless an assistant span is open**: then the start is deferred (see §3.2).
- Closes after `userQuiet` with no input fragment. Emits `EvTranscript{user}` with the joined fragments (this feeds `SigUserSaid`, which the agent uses for confirm words), then `EvSpeechStopped`.

Constants (tunable, measured in the live smoke): `assistantQuiet = 700 ms` (PAI uses 2000 ms; DeskPixie's mouth and "back to listening" feel need less, and a too-early close only costs an extra created/done pair), `userQuiet = 800 ms` (transcripts lag audio), `outputSpeechRMS` = the mouth's open threshold (`mouthOpenAt`, 0.02). Input transcripts lag the audio by a few hundred ms, so `EvSpeechStarted` is late compared with Realtime's VAD; the only effect is that Bit's silence timer stops a little later.

### 3.2 Barge-in without truncate
GPT-Live owns barge-in: it hears the user over its own speech and decides whether to stop (a real interruption) or carry on (a backchannel, background noise). DeskPixie must not second-guess it, and it cannot tell the model what was heard.

1. While an assistant span is open, user transcript fragments **do not** emit `EvSpeechStarted`. They are buffered.
2. If GPT-Live stops talking, the assistant span closes (`EvResponseDone`), then the deferred `EvSpeechStarted` follows. `Conversation` is either already Listening, or still Speaking because local playback hasn't drained. In the second case its existing rule fires `EfInterrupt`: **flush local playback** and send `CmdCancelResponse`. The user stops hearing the tail at once.
3. `CmdCancelResponse` sends **nothing** on the wire. If a span is open, the adapter closes it locally (`EvResponseDone`) and mutes output until it has seen `assistantQuiet` of silence (like Gemini's `muteUntilTurnEnd`). `HeardMs` is dropped: GPT-Live keeps the whole reply in its context (documented limitation [LK]).
4. If GPT-Live keeps talking through user speech (a backchannel), nothing is flushed. Bit's mouth keeps moving, which is what the user hears.

Because output arrives at real-time pace (100 ms frames [PAI]), the local queue holds only network jitter, so the tail after GPT-Live yields is short even before the flush.

### 3.3 Tools: Responses delegation (recommended) vs client delegation
**Recommendation: Responses delegation**, with every tool in `voice.ToolRegistry` declared as a function tool in `delegation.responses.tools`.

Reasons:
1. **It keeps the Tool abstraction (OCP).** It is the only mode with typed function calls (`call_id`, `name`, `arguments`) [PAI, LK]. These map 1:1 onto `EvToolCall` / `CmdToolResult`; `Runtime.invokeTool`, the generation tagging, unknown-tool errors and `ToolRegistry.Specs()` all work unchanged. All existing tools work without edits: `get_time`, `get_weather`, `calculate`, `timer`, `media`, `end_conversation`, `delegate`, `jobs`, `answer_job`, plus `Deps.ExtraTools`.
2. **Client delegation hands the task over without its text.** DeskPixie would have to rebuild intent from transcript fragments and run its own LLM to choose between time, weather and Claude. That is a second router to build, and a worse one.
3. The voice keeps talking while tools run ("Let me check"), which suits `delegate`.

Costs: a second model to pay for (backend tokens, §11), a second prompt, and one extra hop (GPT-Live → backend → tool → backend → GPT-Live). For `get_time` this adds a backend round trip that Realtime didn't have; the live smoke measures it.

**Wire mapping:**
- `session.delegation.created` (responses): mark a delegation outstanding (blocks the assistant span closing).
- `response.event` → nested `response.output_item.done` with `item.type == "function_call"` → `EvToolCall{CallID: call_id, ToolName: name, Args: arguments}` (arguments that aren't valid JSON are wrapped as a JSON string, as the Realtime adapter does).
- Nested `response.completed` / `response.failed` / `response.incomplete`: delegation done (or failed: a non-fatal `EvError` warning; the voice says what it says).
- `CmdToolResult{CallID, Output, Respond}`: `response.item.create` `function_call_output` (`output` = the JSON as a string), then `response.create` **once all outstanding calls of that backend response are answered** (the adapter counts them; `parallel_tool_calls: true` can yield several). For `end_conversation` (`Respond: false`) the output is still sent, so the backend isn't left waiting, but no `response.create`; the conversation is ending.
- Calls the user talked over are **not** answered with errors (unlike Gemini): the backend runs independently of the voice [OA-live], and its late answer is still useful.

**The `Conversation` view of a tool call.** A backend call usually lands while Bit is Listening or Speaking (the voice already said "on it"). `EvToolCall` is fine in any phase today: it raises `pendingTools` and stops the silence timer. `InToolDone` sets `turnReplySeen = false` and expects a follow-up reply. GPT-Live speaks one when the backend result reaches it, which opens an assistant span → `EvResponseCreated`. If it decides not to speak (e.g. the `delegate` ack is enough), the existing 15 s `ReplyWatchdog` takes Bit back to listening. §4 tightens this.

### 3.4 Two prompts
GPT-Live separates the spoken persona from the backend's procedures [OA-prompt]:
- **Live instructions** = `persona.md` + session facts + a GPT-Live section with the three labelled policies the template asks for: "Backchannel policy", "Interruption policy" (stop and listen; a noisy-room line when `voice.noisyRoom`), "Delegation policy" ("Backend tools: …; delegate before answering anything about the time, weather, arithmetic, timers, media, the agent's jobs or research; don't guess while waiting"). Also the notice rules from `delegation.md` that are about *speaking*: how to say "Agent reply:" / "Agent question:" / "Agent progress:" lines, read confirm words exactly.
- **Backend instructions** = the tool-choice rules from `delegation.md` ("call delegate with 'Check: …'", "jobs status/cancel", "answer_job, never for confirm words") + a short "You are Bit's backend; return one or two spoken-style sentences".

`SessionConfig` gains one optional field, `BackendInstructions string`. The composition root fills it from a new asset (`core/voice/openailive/assets/backend.md` plus the agent's backend section when the agent is built). Other adapters ignore it (ISP: a plain field with a zero value, not a new interface). `Instructions` keeps meaning "what the voice model is told".

### 3.5 Agent narration → commentary
`Runtime.Inject(Notice)` keeps its contract: the notice is queued until nobody is talking (`Conversation.tryInject`), then `EfInject` → `CmdInject{Text, Respond: true}`.
- `Respond: true` → `session.commentary.append` {`delegation_id`: null, `content`: text}. GPT-Live paraphrases it aloud (matching the persona rule "paraphrase in at most two sentences").
- `Respond: false` (no caller today) → `session.thinking.append`.
- `content` ≤ 500 tokens: the adapter caps it at 1,500 characters on a sentence boundary (agent replies are already shortened by `agent.Cap`). A longer notice is split into one commentary plus thinking appends.
- **Confirm words.** Commentary is paraphrased, but "Say confirm hawk" must be read word for word. The Live instructions say confirm phrases are read exactly. The adapter sends an "Agent question:" notice that contains "Say confirm" as `session.instructions.append` ("Ask the user now, exactly: …") instead, since instructions are trusted and followed. This is a risk to verify live (§12 Q3).
- Timing: the quiet gate stays. GPT-Live would choose its own moment, but sending only when quiet keeps parity, and `Conversation` already expects a reply to follow an inject (`PhaseThinking` + watchdog).

`HoldOpen` is unchanged. It only changes DeskPixie's silence timeout, but every held second is billed (§11).

## 4. Runtime and state-machine changes

The aim is **no change to `Conversation`'s rules for the existing providers**. The adapter does almost all the work (DIP: the state machine depends on the neutral `Event` contract, not on a vendor). Three small, gated changes:

1. **Typed connect failures (all providers benefit).** Today `Runtime.onConnected` maps every `Connect` error to `FailConnectionLost`. Add in `core/voice`:
   ```go
   // FailureError is an error that knows its tray status line.
   type FailureError interface { error; Failure() Failure }
   ```
   `onConnected` uses `errors.As`; anything else stays `FailConnectionLost`. Add `FailNoAccess` with `Failure.Status()` = `"Voice: this API key has no GPT-Live access"` (text built from `Failure.Msg`, so other providers can reuse the kind). Fatal runtime errors (`EvError{Fatal}`) can carry the same: `Event.Failure *Failure`, used by `onModel` instead of the generic `FailModelError` when set (e.g. `session.closed{reason: "expired"}` → `"Voice: GPT-Live session expired"`, `reason: "content"` → `"Voice: ended by the safety filter"`).
2. **Optional capabilities (ISP).** An optional interface, checked once at `NewRuntime`:
   ```go
   type Capabilities struct {
       Truncate        bool // CmdCancelResponse.HeardMs is meaningful
       OwnsTurnTaking  bool // the provider decides barge-in; speech events are synthesized
   }
   type CapabilityReporter interface { Capabilities() Capabilities }
   ```
   Models that don't implement it get `{Truncate: true}`. With `OwnsTurnTaking`, the runtime passes `ConversationConfig.ProviderTurns = true`, which changes exactly one rule: after `InToolDone` (not end), `turnReplySeen` stays `true` when the phase is Listening, so a tool result that GPT-Live chooses not to speak about doesn't hold Bit in Thinking for 15 s. Everything else is unchanged. The parity tests run with the flag off; new table tests cover it on (§4 item 2).
3. **Usage metrics.** Not a new event kind, which would touch every switch. Instead the adapter takes an optional `UsageSink` (`func(seconds int)`) in its constructor, which the composition root wires to the existing metrics/log. Nothing in `voice` changes.

The mouth, chimes, recording, `PullPlayback` / `ReportPlayback`, generations and signals are untouched.

## 5. Adapter design (`core/voice/openailive`)

Same layering as `core/voice/openai` (SRP per file):

| File | Responsibility | I/O |
|---|---|---|
| `protocol.go` | `sessionStart(cfg, backend)`, `encode(Command) []obj`, `decode([]byte) []serverEvent`. Pure. | none |
| `turns.go` | `turnClock`: spans, deferral, mute-after-cancel, transcript joining, delegation bookkeeping. Pure; `now` injected; `poll(now)` for timeouts. | none |
| `session.go` | dial, handshake, read loop, keepalive (ping 10 s / idle 30 s, as Realtime), `Send`, graceful `Close` (`session.close` → `session.closed` ≤ 1 s → WS close), polling `turnClock` every 50 ms | WebSocket |
| `errors.go` | dial/startup errors → `voice.FailureError` | none |
| `models.go` | `Name = "openai-live"`, `DefaultURL`, `DefaultModel = "gpt-live-1"`, `Voices`, `DefaultBackendModel` | none |

Constructor: `New(opts Options) voice.Model`, `Options{BaseURL, BackendModel, ServiceTier string; Usage func(int)}`; empty values take the defaults. The model also implements `voice.CapabilityReporter` → `{Truncate: false, OwnsTurnTaking: true}`.

Wire details to get right:
- PCM16 LE base64 both ways; keep an odd trailing byte between output deltas (same as the Realtime decoder).
- Ignore unknown event types (forward compatibility); `transport.*` events are sideband-only.
- `error` with a `client_event_id` from an append or audio → non-fatal warning. Errors before `session.started` → connect error (§7). `responses_validation_failed` (bad backend model or tools) → **fatal**, with status `"Voice: GPT-Live backend settings rejected"`, since without the backend Bit has no tools.
- Every client event gets an `event_id` (`c_<n>`) so errors can be matched.

## 6. Settings

| Path | Change |
|---|---|
| `voice.provider` | New choice `openai-live`, labelled **"OpenAI GPT-Live"** (`settings.ProviderOpenAILive`; `providerNames`; registered in `app.Providers()`). `ApplyRestart` as today. |
| `voice.model` | `OpenAILiveModels = [{"gpt-live-1", "GPT-Live 1"}]`. Open choice, as today, so a future id can be typed. |
| `voice.voice` | `OpenAILiveVoices` = the 31 built-in names (§2.2). Open choice: a custom voice id (`voice_…`) can be typed. |
| `ProviderDefaults` | `openai-live: {"gpt-live-1", "marin"}` |
| **new** `voice.liveBackendModel` | "Backend model (GPT-Live)": the Responses model that runs Bit's tools. Open choice: `gpt-6-luna` (default, faster and cheaper), `gpt-6-sol` (harder tasks). Description says it only applies to OpenAI GPT-Live. `ApplyRestart`. |
| `voice.noisyRoom` | Description gains "With GPT-Live this only changes Bit's instructions." |
| Secrets | `providerKeys` gains `{"openai-live", "OPENAI_API_KEY"}` (the same key). `KeyVars()` must dedupe, since it scrubs child-process environments. |

A test keeps the settings lists in step with `openailive.Voices` / `DefaultModel`, as the Gemini one does. `service_tier` is not exposed (default `auto`); see Q4.

## 7. Error cases

| Situation | Detection | Status line / behaviour |
|---|---|---|
| No `OPENAI_API_KEY` | existing secrets path | `Voice: no API key (set OPENAI_API_KEY)` |
| Key invalid | dial HTTP 401, or `error` before `session.started` with type `authentication_error` / `invalid_api_key` | `Voice: OpenAI rejected the API key` (`FailSetup`) |
| **No GPT-Live access** (alpha not enabled, free tier) | dial HTTP 403/404, or a startup `error` whose code is `model_not_found`, `permission_denied`, `insufficient_permissions` or `unsupported_model`, or whose message names the model or access. **The exact codes are undocumented**: the access probe (§10) records the real one and the test table is fixed from it. | `Voice: this API key has no GPT-Live access` (`FailNoAccess`). Settings ▸ Voice shows the same text under `voice.provider`. |
| Free tier / rate limited | `rate_limit_exceeded`, `insufficient_quota`, or too many concurrent sessions | `Voice: OpenAI quota or rate limit reached` |
| Bad backend model / tools | `responses_validation_failed` | fatal: `Voice: GPT-Live backend settings rejected` |
| Strict-config rejection (unknown field) | startup `error` with `param` | `Voice: model error`; logged with code + param (never the key) |
| Moderation cut | `error` mid-session, session stays open | warning; the span closes by silence |
| `session.closed` `expired` / `content` / `connection_lost` / WS drop | read loop | `Voice: GPT-Live session expired` / `Voice: ended by the safety filter` / `Voice: connection lost` |
| Session ends while a tool runs | runtime generation check (existing) | stale result dropped |

## 8. Composition root (`core/app`)

- `Providers()` gains `openailive.Name: func() voice.Model { return openailive.New(...) }`. The factory needs settings (backend model), so `ModelFactory` becomes `func(settings.VoiceSettings) voice.Model` (existing adapters ignore the argument). This is the only signature change outside `core/voice`.
- `SessionConfig(s, instructions)` also fills `BackendInstructions` (the GPT-Live backend asset plus `agent.BackendPersona()` when the agent is built). The `delegation.md` split into speaking rules and tool rules is done once as two assets; Realtime and Gemini keep getting both concatenated, so their prompt is byte-identical to today (test).
- The agent, canvas and Herdr wiring is unchanged: the coordinator still sees `delegate` calls and still answers through `VoiceLink.Inject`. The canvas is reached through `delegate` (the coordinator writes pages), so it needs nothing.

## 9. Behaviour notes and limits

### 9.1 What the user will notice
- Bit may backchannel ("mm-hm") and may keep talking through a short "yeah": that is GPT-Live, by design.
- After an interruption, Bit "remembers" saying the whole reply (no truncate).
- Tool answers go through a backend model, so `get_time` can take a beat longer than on Realtime.
- `voice.noisyRoom` is advisory only.

### 9.2 Idle audio
The mic streams for the whole conversation, so the timeline advances. If the cleaner drops a frame (returns empty), nothing is sent, as today; the 40 ms granularity keeps this harmless. No idle-silence streaming is needed.

### 9.3 Long sessions and reconnect
DeskPixie conversations end after 7 s of silence (180 s while held), far below any plausible duration limit. v1 does **not** reconnect: `expired` or a drop ends the conversation with a status line, and the next hotkey starts a new session. Seeding a new session with `input` history is possible later without interface changes (Q7).

## 10. Access check (run by the user, not now)

A tiny opt-in probe that answers "does this key have GPT-Live?" before any adapter work. File `core/voice/openailive/access_live_test.go`:

```go
//go:build live
// go test -tags live -run TestLiveGPTLiveAccess -v ./voice/openailive/
```

1. Find the key with `voice.NewEnvSecrets(voice.DotenvPaths(appSupport))` for `openai-live` (it never reads `.env` itself and never logs the key); skip when it is absent.
2. Dial `wss://api.openai.com/v1/live/sessions` with `Authorization: Bearer …`, 10 s timeout. On a dial failure, log only the HTTP status and the response body's `error.code` / `error.type`.
3. Send `session.start` with the smallest config: `{"model":"gpt-live-1","delegation":{"type":"client"}}`.
4. Wait up to 10 s for `session.started` or `error`.
5. Log one of: `ACCESS OK (session id prefix, expires_at)`, or `NO ACCESS: <status> <code> <type> <message>`, or `OTHER: …`. The test **passes in every case**: it is a probe, not a gate, and its output is what §7's code table is fixed from.
6. On success send `session.close`, read until `session.closed`, and log `reason` and `usage.seconds`.

Cost: a few seconds of session time (< $0.01); no audio is sent.

## 11. Cost notes

| Item | Rate | Typical DeskPixie use |
|---|---|---|
| Voice session | $0.05/min, per second, **idle included** | A 2-minute chat plus the 7 s silence tail ≈ $0.11. |
| Hold-open while the agent works | same | Each 180 s hold ≈ **$0.15 extra**, even if nobody speaks. Today a held Realtime session costs almost nothing while silent (VAD filters silence). |
| Backend Responses tokens | the backend model's token prices | Every tool turn, plus the context GPT-Live passes to the backend. Small for `gpt-6-luna`. |
| Realtime comparison | tokens, silence nearly free | GPT-Live is cheaper for chatty sessions and dearer for long quiet holds. |

Recommendation: log `usage.seconds` per conversation (via the `UsageSink`) so the user can compare a week of use. Q8 covers whether the hold timeout should be shorter for GPT-Live.

## 12. Open questions for the user

| # | Question | Options | Recommendation |
|---|---|---|---|
| Q1 | Delegation mode | (a) Responses delegation, all tools as backend function tools; (b) client delegation, DeskPixie routes from transcripts; (c) client delegation straight to the Claude coordinator for everything (no backend model, no local tools) | **(a)**: the only mode with typed tool calls; all tools work unchanged (§3.3) |
| Q2 | Backend model default | `gpt-6-luna` / `gpt-6-sol` / `gpt-5.6-luna` (sources differ) | **`gpt-6-luna`** (OpenAI's "start here"), settable in Settings |
| Q3 | Confirm-word prompts ("Say confirm hawk") | (a) `session.instructions.append` with the exact text; (b) commentary plus a persona rule; (c) both, verified live from the output transcript | **(c)**: start with (a), check the live smoke's transcript |
| Q4 | Expose `service_tier` (`priority` = faster backend, dearer)? | no / a toggle | **No** for v1 (`auto`) |
| Q5 | Offer hosted `web_search` on the backend for quick facts, instead of delegating "Check: …" to Claude? | no / yes, as an opt-in setting | **No**: keeps one research path (the coordinator) and the same behaviour across providers |
| Q6 | `voice.noisyRoom` with GPT-Live | (a) a prompt line only; (b) hide the setting for this provider | **(a)** |
| Q7 | Reconnect on drop / `expired` | (a) none, status line; (b) new session seeded with the last ≤ 8k tokens of transcript; (c) `store: true` + fork | **(a)** for v1; (c) stores 30 days of audio, which needs the user's consent |
| Q8 | Hold-open billing | keep 180 s / shorter for GPT-Live / mute and keep the timer | **Keep 180 s**, log usage, revisit after a week |
| Q9 | Turn-clock timings | `assistantQuiet` 700 ms vs PAI's 2000 ms | **700 ms**, tuned from the live smoke |
| Q10 | Is this worth building before access is confirmed? | build behind the probe / wait | **Run the §10 probe first**; build only on `ACCESS OK` |

## 13. Testing

- **Protocol table tests** (`protocol_test.go`, like `openai/protocol_parity_test.go`): the exact `session.start` shape (strict schema: no extra fields; custom voice object; tools from `ToolSpec`; backend block), encode of every `Command`, decode of every server event in §2.3, base64 odd-byte carry, unknown events ignored, startup error codes → failures (§7).
- **`turnClock` tests** (pure, injected clock): span open/close on RMS and gaps; silence fill inside a span; silent frames dropped outside; deferred `SpeechStarted` while the assistant talks (backchannel → no interrupt; yield → `ResponseDone` then `SpeechStarted`); `CmdCancelResponse` mute until quiet; delegation outstanding holds `ResponseDone`; transcripts joined per span; user transcript emitted before `SpeechStopped`.
- **Conformance** (`conformance_test.go`): `voicetest.Run` against an `httptest` fake GPT-Live server, as the Realtime adapter does. The fake acts like GPT-Live: loud input → input transcript fragments; after quiet input it streams a reply as 100 ms output frames (silence frames between replies); for the goodbye clip it emits `session.delegation.created` + a nested `function_call` for `end_conversation` while speaking; it never sends speech or response events. The suite's four steps (turn, cancel, tool before ResponseDone, close) must pass unchanged.
- **Runtime**: new `Conversation` table tests for `ProviderTurns` (§4 item 2); `runtime_parity_test.go` unchanged and green; a test that a `FailureError` from `Connect` reaches the status line.
- **Settings**: lists in step with the adapter; `KeyVars()` dedupes; `openai-live` resolves `OPENAI_API_KEY`; Realtime/Gemini prompts byte-identical after the `delegation.md` split.
- **Live smoke** (`-tags live`, added to `TestLiveVoiceProviders` as a third `liveProvider`): the synthesized "What's the weather in Paris?" clip → a tool call to `get_weather` → spoken answer; a goodbye → `end_conversation`; close with `session.closed` usage logged. Under 30 s, key never printed, skipped without a key or when the access probe's failure codes come back.

## 14. Implementation order (for the plan)

1. Access probe (§10); stop if no access.
2. `core/voice`: `FailureError`, `FailNoAccess`, `Capabilities`, `ConversationConfig.ProviderTurns`, `SessionConfig.BackendInstructions` (tests first).
3. `openailive/protocol.go` + tests; `turns.go` + tests.
4. `session.go` + the fake server + conformance.
5. Settings and secrets; composition root; the prompt split.
6. Live smoke; tune the §3.1 constants; update this spec with measured numbers.
