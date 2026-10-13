# Phase 3 — Skeleton + Go/No-Go Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A talking Bit in the new stack: Swift VoiceProcessingIO mic → Go cleanup (pass-through) → OpenAI Realtime → Swift playback, the mascot drawn natively (Core Animation) with click-through and proximity fade, a tray menu, a global hotkey, and measured go/no-go numbers (latency, underruns, max GC pause).

**Architecture:** A Go core (`core/`) is built as the C-ABI dylib `libdeskpixie` (`-buildmode=c-shared`), following Howl's `libhowl` pattern: JSON configure, Swift **polls** Go for events every 30 ms, and Go never calls into Swift. Audio crosses the boundary as 24 kHz mono int16. Swift pushes 40 ms mic frames and pulls playback samples into its own ring; a `flush` flag on the pull carries barge-in. The Swift app (`mac/`) is an xcodegen project with a local SwiftPM package `DeskPixieCore` that holds every unit-testable piece.

**Tech Stack:** Go 1.26.2, `github.com/coder/websocket`, cgo; Swift 6 (strict concurrency), SwiftUI `MenuBarExtra`, AppKit `NSPanel`, WebKit, AudioToolbox (VoiceProcessingIO), Carbon `RegisterEventHotKey`; xcodegen.

**Spec:** `docs/specs/2026-10-13-deskpixie-port-design.md` (§4, §5 Phase 3, §7).

**Source to port from:** ai-mascot at `~/Documents/Projects/ai-moscot`, ref `origin/main` (33dcf70). Read files with `git -C ~/Documents/Projects/ai-moscot show origin/main:<path>`. **Never read any `.env` file.** Howl is at `~/Documents/Projects/voice-keyboard` (read-only reference).

## Global Constraints

- Bundle id `com.sihekuang.deskpixie`; product name "DeskPixie"; mascot name "Bit"; Go module `github.com/sihekuang/deskpixie/core`.
- macOS only, arm64, deployment target macOS 14.0, `SWIFT_VERSION 6.0`, `SWIFT_STRICT_CONCURRENCY: complete`.
- All app audio is **24 000 Hz, mono, signed int16 little-endian**. Mic frames are **960 samples (40 ms)**.
- Interfaces at every external boundary (SOLID): `audio.Cleaner`, `voice.Model`/`voice.Session`, Swift `CoreEngine`, `VoiceIO`.
- Never print, log or commit API keys. Phase 3 reads `OPENAI_API_KEY` from the process environment, or else from `~/Library/Application Support/com.sihekuang.deskpixie/.env` (Keychain comes in Phase 4).
- Default hotkey: **⌃⌥Space** (the Rust app's `ctrl+alt+Space`).
- Default model `gpt-realtime`, voice `marin`, idle timeout 7 s, chimes are out of scope for Phase 3.
- The mascot is rendered natively in Swift (decision 2026-10-13, replaces spec §4.3's WKWebView); packs (`mascots/*/mascot.json` + `sheet.png`) are unchanged.
- Path-scoped commits (`git add <paths>`), never `git add -A`, never bare `git stash`. Each commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Each task closes its GitHub issue in the commit message (`Closes #N`).

## Review Focus

1. **Barge-in while audio is queued on both sides**: the user speaks while Bit has seconds of audio buffered in Go *and* in the Swift ring. Expected: playback stops within ~50 ms, and `conversation.item.truncate` gets `audio_end_ms` equal to what was actually *played*, not what was queued. Covered in Task 5 (`TestRuntimeBargeInFlushesAndTruncates`) and Task 7 (`PlaybackRingTests.testFlushDropsBuffered`).
2. **Missing or invalid API key**: Expected: Bit shows `error`, the tray shows "OPENAI_API_KEY not set", and nothing crashes. Covered in Task 6 (`TestBridgeConfigureWithoutKey`).
3. **WebSocket drops mid-conversation** (network change, 30 s silence): Expected: a fatal error moves the runtime to Idle via Cooldown and the next hotkey reconnects. Covered in Task 4 (`TestSessionServerCloseEmitsFatal`) and Task 3 (ported cooldown cases).
4. **Mic permission denied**: Expected: the error state plus a tray status line, no hang. Covered in Task 8 (`VoiceIO.start` throws `.permissionDenied`; the coordinator maps it to `dp_mic_failed`).
5. **The C boundary under load**: Swift pushes frames faster than Go drains them (GC pause). Expected: frames drop with a counted warning and Swift never blocks. Covered in Task 6 (`TestBridgePushAudioNeverBlocks`).

---

## File Structure

```
core/
  go.mod  Makefile
  audio/      cleaner.go (Cleaner iface, Passthrough), pcm.go (i16<->LE bytes, RMS)
  voice/      types.go (Event, Command, MascotState, SessionConfig, Model, Session)
              conversation.go (pure state machine, port of voice-core conversation.rs)
              mouth.go (MouthMeter)  runtime.go (glues Session+Cleaner+playback)
              persona.go + assets/persona.md
  voice/openai/  session.go protocol.go
  packs/      packs.go (manifest load, roots, selection)
  metrics/    metrics.go (latency, GC pause, underruns)
  bridge/     bridge.go (pure-Go engine API that the C layer wraps; testable)
  cmd/libdeskpixie/  exports.go main.go
mac/
  project.yml  Makefile  Configs/SharedSettings.xcconfig
  DeskPixie/   DeskPixieApp.swift AppDelegate.swift Info.plist DeskPixie.entitlements
               Composition/CompositionRoot.swift  Engine/EngineCoordinator.swift
               Mascot/{MascotPanel,MascotView,MascotController}.swift
               MenuBar/MenuBarMenu.swift
  Packages/DeskPixieCore/  Package.swift
    Sources/CDP/include/{module.modulemap,libdeskpixie_shim.h}
    Sources/CDPStubs/cdp_stubs.c
    Sources/DeskPixieCore/Bridge/{CoreEngine.swift,LibDeskPixieEngine.swift,EngineEvent.swift}
    Sources/DeskPixieCore/Audio/{PlaybackRing.swift,VoiceIO.swift,VPIOVoiceIO.swift,AudioPump.swift}
    Sources/DeskPixieCore/Hotkey/CarbonHotkey.swift
    Sources/DeskPixieCore/Mascot/{Pack,MascotPlayer,Gesture,SpriteSheet,ProximityFade}.swift
    Tests/DeskPixieCoreTests/...
mascots/     bit/ dino/ blackgold/ flash/          (copied unchanged)
docs/go-no-go.md
```

---

### Task 1: Go module, audio primitives, Makefile  (issue #6)

**Files:**
- Create: `core/go.mod`, `core/Makefile`, `core/audio/cleaner.go`, `core/audio/pcm.go`
- Test: `core/audio/audio_test.go`

**Interfaces:**
- Produces:
  ```go
  package audio
  const SampleRate = 24000
  const FrameSamples = 960
  type Cleaner interface { Process(in []int16) []int16; Close() error }
  type Passthrough struct{}            // Process returns in unchanged
  func EncodeLE(s []int16) []byte
  func DecodeLE(b []byte) (samples []int16, rest []byte) // odd trailing byte returned in rest
  func RMS(s []int16) float32          // normalised 0..1 (divide by 32768)
  ```

- [ ] **Step 1: Init the module.** `cd core && go mod init github.com/sihekuang/deskpixie/core && go mod edit -go=1.26.2`
- [ ] **Step 2: Write failing tests** in `core/audio/audio_test.go`:
  ```go
  func TestPassthroughReturnsInput(t *testing.T) {
      in := []int16{1, -2, 3}
      if got := (Passthrough{}).Process(in); !slices.Equal(got, in) { t.Fatalf("got %v", got) }
  }
  func TestEncodeDecodeRoundTrip(t *testing.T) {
      in := []int16{0, 1, -1, 32767, -32768}
      out, rest := DecodeLE(EncodeLE(in))
      if !slices.Equal(out, in) || len(rest) != 0 { t.Fatalf("got %v rest %v", out, rest) }
  }
  func TestDecodeCarriesOddByte(t *testing.T) {
      out, rest := DecodeLE([]byte{0x01, 0x00, 0x7f})
      if !slices.Equal(out, []int16{1}) || !bytes.Equal(rest, []byte{0x7f}) { t.Fatal(out, rest) }
  }
  func TestRMS(t *testing.T) {
      if RMS(nil) != 0 { t.Fatal("empty") }
      if r := RMS([]int16{16384, -16384}); math.Abs(float64(r)-0.5) > 1e-3 { t.Fatal(r) }
  }
  ```
- [ ] **Step 3:** `cd core && go test ./audio/` → FAIL (undefined).
- [ ] **Step 4: Implement** `cleaner.go` and `pcm.go` per the interfaces (`binary.LittleEndian`, `math.Sqrt`).
- [ ] **Step 5:** `go test ./audio/` → PASS.
- [ ] **Step 6: Makefile** (`core/Makefile`):
  ```make
  .PHONY: build-dylib test clean
  build-dylib:
  	go build -buildmode=c-shared -o build/libdeskpixie.dylib ./cmd/libdeskpixie
  	install_name_tool -id "@rpath/libdeskpixie.dylib" build/libdeskpixie.dylib
  test:
  	go test ./...
  clean:
  	rm -rf build
  ```
  `build-dylib` will fail until Task 6; that is expected.
- [ ] **Step 7: Commit** `core/go.mod core/Makefile core/audio` with "feat(core): Go module, audio.Cleaner and PCM helpers".

---

### Task 2: Voice types + persona  (issue #7)

**Files:**
- Create: `core/voice/types.go`, `core/voice/persona.go`, `core/voice/assets/persona.md`
- Test: `core/voice/persona_test.go`

**Interfaces:**
- Produces (Go port of `crates/voice-core/src/types.rs` + `traits.rs`, read them):
  ```go
  package voice
  type MascotState string
  const (StateIdle MascotState = "idle"; StateListening = "listening"; StateThinking = "thinking"
         StateDelegating = "delegating"; StateHappy = "happy"; StateError = "error")
  type TurnDetection int
  const (TurnSemantic TurnDetection = iota; TurnServerVAD; TurnManual)
  type ToolSpec struct { Name, Description string; Parameters json.RawMessage }
  type SessionConfig struct { Model, Voice, Instructions string; Tools []ToolSpec
      TurnDetection TurnDetection; NoisyRoom, ProviderNoiseReduction bool }
  type EventKind int
  const (EvReady EventKind = iota; EvSpeechStarted; EvSpeechStopped; EvAudioDelta; EvResponseCreated
         EvResponseDone; EvToolCall; EvTranscript; EvError)
  type Event struct { Kind EventKind; Audio []int16; CallID, ToolName string; Args json.RawMessage
      Role, Text string; Fatal bool; Message string }
  type CommandKind int
  const (CmdAudio CommandKind = iota; CmdCancelResponse; CmdToolResult; CmdInject; CmdClose)
  type Command struct { Kind CommandKind; Audio []int16; HeardMs int64; CallID string
      Output json.RawMessage; Respond bool; Text string }
  type Session interface { Send(Command) error; Events() <-chan Event; Close() error }
  type Model interface { Name() string; Connect(ctx context.Context, cfg SessionConfig, apiKey string) (Session, error) }
  func DefaultPersona() string                       // embedded assets/persona.md
  func Instructions(personaPath string) string        // file contents, or DefaultPersona() when empty/unreadable/blank
  ```
- [ ] **Step 1:** Copy the persona: `git -C ~/Documents/Projects/ai-moscot show origin/main:crates/voice-core/assets/persona.md > core/voice/assets/persona.md`
- [ ] **Step 2: Failing tests:**
  ```go
  func TestDefaultPersonaIsBit(t *testing.T) { if !strings.Contains(DefaultPersona(), "You are Bit") { t.Fatal() } }
  func TestInstructionsFallsBack(t *testing.T) {
      if Instructions("") != DefaultPersona() { t.Fatal("empty path") }
      if Instructions("/nonexistent/x.md") != DefaultPersona() { t.Fatal("missing file") }
      p := filepath.Join(t.TempDir(), "p.md"); os.WriteFile(p, []byte("  \n"), 0o600)
      if Instructions(p) != DefaultPersona() { t.Fatal("blank file") }
      os.WriteFile(p, []byte("Be terse."), 0o600)
      if Instructions(p) != "Be terse." { t.Fatal("custom") }
  }
  ```
- [ ] **Step 3:** `go test ./voice/` → FAIL. **Step 4:** implement (`//go:embed assets/persona.md`). **Step 5:** PASS.
- [ ] **Step 6: Commit** `core/voice` — "feat(core): voice types and persona".

---

### Task 3: Conversation state machine (port tests first)  (issue #8)

**Files:**
- Create: `core/voice/conversation.go`
- Test: `core/voice/conversation_test.go`

**Source:** `crates/voice-core/src/conversation.rs` (1,147 lines incl. tests). It is pure: `step(Input, now) -> Vec<Effect>`. Port it one-for-one.

**Interfaces:**
- Produces:
  ```go
  type Phase int  // PhaseIdle, PhaseConnecting, PhaseListening, PhaseThinking, PhaseSpeaking, PhaseCooldown
  type InputKind int // InHotkey, InReady, InSpeechStarted, InSpeechStopped, InAudioDelta, InResponseCreated,
                     // InResponseDone, InToolCall, InToolDone, InPlaybackIdle, InError, InTick, InMicFailed,
                     // InMicLive, InHold  — use exactly the variant set of the Rust `Input` enum, Go-named.
  type Input struct { Kind InputKind; Fatal bool; Hold bool; /* fields mirroring the Rust variants */ }
  type EffectKind int // EfConnect, EfClose, EfStartMic, EfStopMic, EfPlay, EfFlush, EfInterrupt,
                      // EfResponseStarted, EfMascot, EfStatus, ... — exactly the Rust `Effect` set.
  type Effect struct { Kind EffectKind; State MascotState; Status string /* mirroring Rust */ }
  type Conversation struct { /* phase, timers, counters */ }
  func NewConversation(cfg ConversationConfig) *Conversation   // IdleTimeout 7s, HoldOpen 180s, ReplyWatchdog 15s, MicWait 60s, Cooldown 2s
  func (c *Conversation) Step(in Input, now time.Time) []Effect
  func (c *Conversation) Phase() Phase
  ```
  Chimes effects may be kept as effect kinds; the Phase 3 runtime ignores them.

- [ ] **Step 1: Port every `#[test]` in `conversation.rs`'s `mod tests`** to Go table tests in `conversation_test.go`, same names in Go case, same inputs, same expected effects/phases. Do not drop or weaken a case. Include at least: hotkey from Idle → Connecting + mascot thinking; Ready → Listening + StartMic + mascot listening; SpeechStopped → Thinking; first AudioDelta → Speaking + ResponseStarted + mascot idle; SpeechStarted while Speaking → Listening + Interrupt; turn end when no open responses, no pending tools, playback idle → Listening; idle timeout 7 s while Listening ends the conversation; hold-open uses 180 s; reply watchdog 15 s; fatal error → Cooldown 2 s → Idle; hotkey while Connecting → Idle.
- [ ] **Step 2:** `go test ./voice/ -run Conversation` → FAIL.
- [ ] **Step 3: Implement** `conversation.go` by translating the Rust `step` match arms.
- [ ] **Step 4:** `go test ./voice/` → PASS. Run `grep -c '#\[test\]'` on the Rust file's test module and confirm the Go case count matches; note any intentional omission in a code comment with the reason.
- [ ] **Step 5: Commit** `core/voice/conversation*.go` — "feat(core): port conversation state machine with its tests".

---

### Task 4: OpenAI Realtime adapter  (issue #9)

**Files:**
- Create: `core/voice/openai/protocol.go`, `core/voice/openai/session.go`
- Test: `core/voice/openai/session_test.go`

**Source:** `crates/voice-openai/src/{session.rs,protocol.rs}`. Protocol summary (GA API, no `OpenAI-Beta` header):
- URL `wss://api.openai.com/v1/realtime?model={model}`; model must match `^[A-Za-z0-9._-]+$`. Header `Authorization: Bearer <key>`.
- On connect send `session.update` immediately; `Connect` returns only after the first `session.updated` (10 s timeout).
- `session.update`:
  ```json
  {"type":"session.update","session":{"type":"realtime","instructions":"...","output_modalities":["audio"],
   "audio":{"input":{"format":{"type":"audio/pcm","rate":24000},
                     "transcription":{"model":"gpt-4o-mini-transcribe"},
                     "turn_detection":{"type":"semantic_vad","eagerness":"auto","create_response":true,"interrupt_response":true}},
            "output":{"format":{"type":"audio/pcm","rate":24000},"voice":"marin"}},
   "tools":[...],"tool_choice":"auto"}}
  ```
  ServerVAD: `{"type":"server_vad","create_response":true,"interrupt_response":true}`; NoisyRoom: server_vad with `threshold 0.85, prefix_padding_ms 300, silence_duration_ms 500`; Manual: `null`. Add `audio.input.noise_reduction={"type":"far_field"}` only when `ProviderNoiseReduction`.
- Send: `input_audio_buffer.append {audio: base64(EncodeLE)}`; CancelResponse → `response.cancel` (only if a response is active) then `conversation.item.truncate {item_id: <last output_audio.delta item_id>, content_index:0, audio_end_ms: HeardMs}`; ToolResult → `conversation.item.create {item:{type:"function_call_output",call_id,output}}` + `response.create` if Respond; Inject → `conversation.item.create` user `input_text` + `response.create` if Respond; Close → close socket.
- Receive mapping: `session.updated`→(first only) Ready; `input_audio_buffer.speech_started/stopped`; `response.created`; `response.output_audio.delta` → AudioDelta (base64 → DecodeLE, carry odd byte across deltas, remember item_id); `response.output_audio_transcript.done` → Transcript{assistant}; `conversation.item.input_audio_transcription.completed` → Transcript{user}; `response.done` → ToolCall for each `output[]` item `type=="function_call" && status=="completed"` when response status is `completed`, then ResponseDone; status `failed` → Error{Fatal:true}; `error` → Error{Fatal:false} except code `response_cancel_not_active` (ignored).
- Keepalive: ping every 10 s; 30 s with nothing received → Error{Fatal:true} and close. Writes time out after 5 s. Socket closed by peer → Error{Fatal:true}.

**Interfaces:**
- Consumes: `voice.Model`, `voice.Session`, `voice.SessionConfig`, `audio.EncodeLE/DecodeLE`.
- Produces: `func New(baseURL string) voice.Model` (`baseURL` default `wss://api.openai.com/v1/realtime`; tests pass an `httptest` URL).

- [ ] **Step 1:** `go get github.com/coder/websocket@latest`
- [ ] **Step 2: Failing tests** with an `httptest.Server` that upgrades via `websocket.Accept` and scripts the server side:
  - `TestConnectSendsSessionUpdateAndWaitsForUpdated`: assert the first client message is `session.update` with `rate:24000`, voice `marin`, `semantic_vad`; server replies `session.updated`; `Connect` returns and the first event is `EvReady`; header `Authorization` == `Bearer test-key`.
  - `TestConnectRejectsBadModel`: model `"gpt realtime"` → error, no dial.
  - `TestAudioAppendIsBase64LE`: `Send(CmdAudio{1,-1})` → server sees `input_audio_buffer.append` with `audio == base64([0x01,0x00,0xff,0xff])`.
  - `TestAudioDeltaCarriesOddByte`: server sends two deltas with 3 and 1 bytes → client events give samples totalling 2.
  - `TestCancelSendsCancelThenTruncate`: after a delta with `item_id:"it1"` and `response.created`, `Send(CmdCancelResponse{HeardMs:1234})` → server receives `response.cancel` then `conversation.item.truncate` with `item_id:"it1", audio_end_ms:1234`.
  - `TestResponseDoneEmitsToolCalls`: completed response with one `function_call` → `EvToolCall{CallID, ToolName, Args}` then `EvResponseDone`.
  - `TestCancelNotActiveErrorIgnored` and `TestOtherErrorIsNonFatal`.
  - `TestSessionServerCloseEmitsFatal`: server closes → `EvError{Fatal:true}`.
- [ ] **Step 3:** `go test ./voice/openai/` → FAIL. **Step 4:** implement. **Step 5:** PASS (run with `-race`).
- [ ] **Step 6: Commit** `core/voice/openai core/go.mod core/go.sum` — "feat(core): OpenAI Realtime adapter".

---

### Task 5: Runtime + mouth meter  (issue #10)

**Files:**
- Create: `core/voice/mouth.go`, `core/voice/runtime.go`
- Test: `core/voice/runtime_test.go`, `core/voice/mouth_test.go`

**Source:** `crates/voice-core/src/runtime.rs` (33 ms tick, mouth meter), simplified: no tools beyond none, no delegation hold in Phase 3.

**Design:** The runtime owns a Go-side **playback queue** (`[]int16` behind a mutex) that Swift drains via `PullPlayback`. Swift reports how many samples it has actually rendered and how many it still has buffered via `ReportPlayback`. Barge-in sets a `flushPending` flag, clears the Go queue, and the next `PullPlayback` returns `flush=true` so Swift drops its ring.
- `heard` for truncation = `played - responseMark`, where `responseMark` = `played` when the response's first AudioDelta arrived; convert to ms at 24 kHz.
- `playbackIdle` = Go queue empty && Swift buffered == 0.
- Mouth: `MouthMeter{attack 0.6, release 0.25, open 0.02, step 0.08, levels}`; port `crates/voice-core/src/mouth.rs` (or wherever `MouthMeter` lives — `git grep -n MouthMeter origin/main`) **with its tests**. Mouth is 0 outside Speaking.

**Interfaces:**
- Consumes: `Conversation`, `voice.Model`, `audio.Cleaner`.
- Produces:
  ```go
  type UIEvent struct { Kind string `json:"kind"`; State string `json:"state,omitempty"`; Level int `json:"level,omitempty"`
      Text string `json:"text,omitempty"`; Role string `json:"role,omitempty"` }   // kinds: "state","mouth","status","transcript","error","metrics"
  type RuntimeConfig struct { Session SessionConfig; APIKey string; MouthLevels int; Conversation ConversationConfig }
  type Runtime struct{...}
  func NewRuntime(m Model, c audio.Cleaner, cfg RuntimeConfig, emit func(UIEvent)) *Runtime
  func (r *Runtime) Hotkey()
  func (r *Runtime) PushMic(frame []int16, capturedAt time.Time)   // non-blocking; drops when full (counts drops)
  func (r *Runtime) PullPlayback(dst []int16) (n int, flush bool)
  func (r *Runtime) ReportPlayback(playedSamples int64, bufferedSamples int, rms float32)
  func (r *Runtime) MicFailed(reason string)
  func (r *Runtime) Close()
  ```
  The runtime goroutine: select on mic chan (→ cleaner.Process → `Session.Send(CmdAudio)` only while the conversation wants the mic), session events (→ `Conversation.Step`), a 33 ms ticker (→ InTick, InPlaybackIdle when idle, mouth update), hotkey chan. Effects: Connect → `Model.Connect` in a goroutine, feeding back InReady/InError; StartMic/StopMic → flip `micActive` and emit `{"kind":"mic","text":"start"|"stop"}`; Play → append to queue; Interrupt → flush + `CmdCancelResponse{HeardMs}`; Mascot → emit state; Status → emit status; Close → `Session.Close`.
  No API key → emit `{"kind":"error","text":"OPENAI_API_KEY not set"}` and state `error` on hotkey, no connect.

- [ ] **Step 1: Failing tests** with a `fakeModel`/`fakeSession` (channels you script from the test):
  - `TestRuntimeHotkeyConnectsAndListens`: Hotkey → fake Connect called with the config; fake emits Ready → UI events contain state `thinking` then `listening` and `mic:start`.
  - `TestRuntimeMicOnlySentWhileActive`: PushMic before Ready → fake session receives no audio; after Ready → receives `CmdAudio` with the same samples (Passthrough cleaner).
  - `TestRuntimePlaybackQueue`: fake emits AudioDelta of 2400 samples → `PullPlayback(buf[:1000])` returns 1000, then 1000, then 400, then 0.
  - `TestRuntimeBargeInFlushesAndTruncates`: AudioDelta 48000 samples; `ReportPlayback(played: 12000, buffered: 4800)`; fake emits SpeechStarted → next `PullPlayback` returns `flush=true, n=0`; fake session receives `CmdCancelResponse{HeardMs: 500}`.
  - `TestRuntimeNoKeyShowsError`: empty APIKey → Hotkey emits state `error` + error text, fake Connect not called.
  - `TestRuntimePushMicNeverBlocks`: with the runtime loop stalled (fake session Send blocks), 1000 PushMic calls return within 50 ms.
  - Port the MouthMeter tests from Rust into `mouth_test.go`.
- [ ] **Step 2:** FAIL. **Step 3:** implement. **Step 4:** `go test -race ./voice/` PASS.
- [ ] **Step 5: Commit** `core/voice/runtime*.go core/voice/mouth*.go` — "feat(core): voice runtime with playback queue and barge-in".

---

### Task 6: Packs, metrics, bridge, libdeskpixie C-ABI  (issue #11)

**Files:**
- Create: `core/packs/packs.go`, `core/metrics/metrics.go`, `core/bridge/bridge.go`, `core/cmd/libdeskpixie/{exports.go,main.go}`
- Test: `core/packs/packs_test.go`, `core/metrics/metrics_test.go`, `core/bridge/bridge_test.go`
- Copy: `mascots/` from ai-mascot (all four packs) into repo root `mascots/`.

**Packs:** Port the `ResolvedPack` shape and resolution from `src-tauri/src/packs.rs` and `crates/mascot-pack` (manifest `contract:1`, `cell`, `sheet`, `fps`, `bob{px,frames}`, `states{row,frames,loop,bob,faceDy}`, `mouth{row,anchor,size,levels}`). JSON output must equal what the Rust app sends in `mascot://pack`'s `pack` field (camelCase; check the serde attributes). Roots in order: bundled dir, then user dir; a user pack overrides a bundled one with the same id; selection: wanted → `bit` → first valid (sorted by id). Invalid manifests are skipped.
```go
type Pack struct { ID string `json:"-"`; Dir string `json:"-"`; SheetPath string `json:"-"`; /* ResolvedPack fields */ }
func Load(roots ...string) (map[string]Pack, error)
func Select(packs map[string]Pack, wanted string) (Pack, bool)
```
Tests: load the real `mascots/` dir → 4 packs, `bit` has cell [32,32] and mouth levels 4; override (temp user root with `bit` named "Mine" wins); `Select("nope")` → bit; broken JSON skipped.

**Metrics:**
```go
type Recorder struct{...}
func (r *Recorder) MicToSend(d time.Duration)   // capture timestamp → websocket write
func (r *Recorder) Underruns(total int64)        // reported by Swift
func (r *Recorder) Snapshot() Snapshot           // {MicToSendP50Ms, MicToSendP95Ms, MaxGCPauseMs, Underruns}
```
Max GC pause from `runtime/metrics` `/sched/pauses/total/gc:seconds` histogram (highest non-empty bucket). Emit a `{"kind":"metrics","text":<json snapshot>}` UI event every 10 s and append it to `~/Library/Logs/DeskPixie/metrics.log`. Wire `MicToSend` in the runtime: pass `capturedAt` through to the point after `Session.Send(CmdAudio)` returns. Tests: percentiles of a known sample set; `MaxGCPauseMs > 0` after `runtime.GC()` ×3.

**Bridge** (pure Go, all logic; the cgo layer only converts types):
```go
type Config struct { APIKey string `json:"apiKey"`; Model string `json:"model"`; Voice string `json:"voice"`
    PersonaPath string `json:"personaPath"`; IdleTimeoutSecs int `json:"idleTimeoutSecs"`
    Pack string `json:"pack"`; PackRoots []string `json:"packRoots"`; NoisyRoom bool `json:"noisyRoom"` }
func Init() ; func Configure(jsonCfg []byte) error  // defaults: gpt-realtime, marin, 7, "bit"
func Hotkey(); func PushAudio(s []int16, capturedUnixNanos int64); func PullAudio(dst []int16) (int, bool)
func ReportPlayback(played int64, buffered int, rms float32, underruns int64)
func MicFailed(reason string); func PollEvent() []byte /* nil when empty */; func PackJSON() []byte; func LastError() string; func Destroy()
```
Events go into a buffered chan (256); when full, drop the oldest `mouth` event first, never block. `PackJSON` returns `{"pack":{...},"sheetPath":"/abs/sheet.png"}` (Swift builds the data URL).

**C ABI** (`exports.go`, `//export`, `#include <stdlib.h>` + `<stdint.h>`):
```c
int   dp_init(void);
int   dp_configure(char* json);            // 0 ok, 1 not init, 2 JSON error, 3 build error
void  dp_hotkey(void);
int   dp_push_audio(const int16_t* samples, int count, int64_t captured_unix_nanos); // copies; 0 ok, 1 dropped
int   dp_pull_audio(int16_t* dst, int max, int* flush);   // returns n written
void  dp_report_playback(int64_t played, int buffered, float rms, int64_t underruns);
void  dp_mic_failed(char* reason);
char* dp_poll_event(void);                 // JSON or NULL; free with dp_free_string
char* dp_pack_json(void);
char* dp_last_error(void);
void  dp_free_string(char* s);
void  dp_destroy(void);
```
Bridge tests: `TestBridgeConfigureWithoutKey` (Configure ok, Hotkey → events contain state `error`); `TestBridgeConfigureBadJSON` (error, LastError non-empty); `TestBridgePushAudioNeverBlocks` (10 000 pushes < 100 ms); `TestBridgePackJSON` (pack name "Bit", sheetPath exists).

- [ ] **Step 1:** Copy packs: `for p in bit dino blackgold flash; do mkdir -p mascots/$p; for f in mascot.json sheet.png; do git -C ~/Documents/Projects/ai-moscot show origin/main:mascots/$p/$f > mascots/$p/$f; done; done`
- [ ] **Step 2:** Failing tests for packs, metrics, bridge (above). **Step 3:** FAIL. **Step 4:** implement. **Step 5:** `go test -race ./...` PASS.
- [ ] **Step 6:** `make -C core build-dylib` → `core/build/libdeskpixie.dylib` + `.h`; `nm -gU core/build/libdeskpixie.dylib | grep _dp_` lists all 13 symbols.
- [ ] **Step 7: Commit** `core/packs core/metrics core/bridge core/cmd mascots core/go.*` — "feat(core): libdeskpixie C-ABI, packs and metrics".

---

### Task 7: Swift package DeskPixieCore — C module, engine bridge, playback ring  (issue #12)

**Files:**
- Create: `mac/Packages/DeskPixieCore/Package.swift`, `Sources/CDP/include/{module.modulemap,libdeskpixie_shim.h}`, `Sources/CDPStubs/cdp_stubs.c`, `Sources/DeskPixieCore/Bridge/{CoreEngine.swift,LibDeskPixieEngine.swift,EngineEvent.swift}`, `Sources/DeskPixieCore/Audio/PlaybackRing.swift`
- Test: `Tests/DeskPixieCoreTests/{PlaybackRingTests.swift,EngineEventTests.swift}`

**Pattern:** Howl's `mac/Packages/HowlCore` (read `Package.swift`, `Sources/CVKB`, `Sources/CVKBStubs`, `Bridge/CoreEngine.swift`, `Bridge/LibhowlEngine.swift`). `libdeskpixie_shim.h` is a hand-written copy of the 13 prototypes from Task 6. `cdp_stubs.c` defines each symbol returning 0/NULL so `swift test` links without the dylib (only the test target depends on CDPStubs).

**Interfaces:**
```swift
public struct EngineConfig: Codable, Sendable { apiKey, model, voice, personaPath: String?, idleTimeoutSecs: Int, pack: String, packRoots: [String], noisyRoom: Bool }
public enum EngineEvent: Sendable, Equatable { case state(String), mouth(Int), status(String), transcript(role: String, text: String), error(String), mic(start: Bool), metrics(String) }
// EngineEvent.decode(_ json: Data) -> EngineEvent?  (kind-switch over the UIEvent JSON from Task 5)
public protocol CoreEngine: Sendable {
    func configure(_ c: EngineConfig) throws
    func hotkey()
    nonisolated func pushAudio(_ s: UnsafeBufferPointer<Int16>, capturedAt: UInt64) -> Bool
    nonisolated func pullAudio(into: UnsafeMutableBufferPointer<Int16>) -> (count: Int, flush: Bool)
    nonisolated func reportPlayback(played: Int64, buffered: Int, rms: Float, underruns: Int64)
    func micFailed(_ reason: String)
    func pollEvent() -> EngineEvent?
    func packJSON() -> Data?
    func shutdown()
}
public final class LibDeskPixieEngine: CoreEngine   // @unchecked Sendable; thin wrapper over dp_*
public final class PlaybackRing: @unchecked Sendable {  // SPSC lock-free (Atomics via `Synchronization.Atomic`)
    public init(capacity: Int)                          // samples
    public func write(_ s: UnsafeBufferPointer<Int16>) -> Int   // producer (pump thread)
    public func read(into: UnsafeMutableBufferPointer<Int16>) -> Int  // consumer (render callback); zero-fills the rest, counts an underrun when it had to zero-fill while `expectingAudio`
    public func flush()                                 // producer side: drop everything buffered
    public var buffered: Int { get }; public var played: Int64 { get }; public var underruns: Int64 { get }; public var lastRMS: Float { get }
}
```
Flush must be safe while the render thread reads: use the Rust counters scheme (`written`, `read`, `flushTo`; `flush()` sets `flushTo = written`; `read` first skips to `flushTo` if behind). `played` counts only samples actually copied out (not zero fill).

- [ ] **Step 1: Failing tests:** `PlaybackRingTests`: `testWriteThenRead`, `testZeroFillAndUnderrunCount`, `testFlushDropsBuffered` (write 4800, read 1200, flush → buffered 0, next read returns zeros, played == 1200), `testPlayedExcludesZeroFill`, `testWrapAround`. `EngineEventTests`: decode each `kind` JSON from Task 5; unknown kind → nil.
- [ ] **Step 2:** `cd mac/Packages/DeskPixieCore && swift test` → FAIL. **Step 3:** implement. **Step 4:** PASS.
- [ ] **Step 5: Commit** `mac/Packages/DeskPixieCore` — "feat(mac): DeskPixieCore package with engine bridge and playback ring".

---

### Task 8: VoiceProcessingIO audio + pump  (issue #13)

**Files:**
- Create: `Sources/DeskPixieCore/Audio/{VoiceIO.swift,VPIOVoiceIO.swift,AudioPump.swift}`
- Test: `Tests/DeskPixieCoreTests/AudioPumpTests.swift`

**Source:** `crates/audio-macos/src/{engine.rs,speaker.rs,counters.rs,mic_pump.rs}` — port the AudioUnit setup:
- One `kAudioUnitSubType_VoiceProcessingIO` unit; EnableIO input element 1 and output element 0; stream format on both sides: 24000 Hz, `kAudioFormatLinearPCM`, signed int16 packed, mono.
- `kAUVoiceIOProperty_OtherAudioDuckingConfiguration` = `{mEnableAdvancedDucking: true, mDuckingLevel: .min}`.
- Input callback: `AudioUnitRender` into a preallocated buffer → `SampleRing` (2 s capacity), recording `mach_absolute_time()` of the newest sample.
- Define `public final class SampleRing: @unchecked Sendable { init(capacity: Int); func write(_:) -> Int; func read(into:) -> Int /* no zero-fill */; var available: Int; var newestTimestamp: UInt64 }` in `Audio/SampleRing.swift` (SPSC, same atomics scheme as `PlaybackRing`; extract a shared core if it stays simple). Test: `SampleRingTests.testWriteReadWrap`, `testOverflowDropsNewest`.
- Render callback: `playback.read(into:)`. No allocation, no locks, no Swift concurrency on the audio threads.
- Mic permission: `AVCaptureDevice.requestAccess(for: .audio)`; denied → throw `VoiceIOError.permissionDenied`.
- `LSUIElement` app: no device pinning in Phase 3 (system default devices).

**Interfaces:**
```swift
public protocol VoiceIO: AnyObject, Sendable {
    func start(playback: PlaybackRing, onMicSamples: @escaping @Sendable (UnsafeBufferPointer<Int16>, UInt64) -> Void) async throws
    func setMicEnabled(_ on: Bool)
    func stop()
}
public final class VPIOVoiceIO: VoiceIO
public final class AudioPump {   // dedicated Thread, 10 ms period
    public init(engine: CoreEngine, ring: PlaybackRing, mic: SampleRing, targetBufferedSamples: Int = 4800)
    public func start(); public func stop()
    public func tick()   // exposed for tests: 1) drain mic ring in 960-sample frames → engine.pushAudio
                         // 2) pull from engine until ring.buffered >= target (flush → ring.flush() first)
                         // 3) engine.reportPlayback(ring.played, ring.buffered, ring.lastRMS, ring.underruns)
}
```
- [ ] **Step 1: Failing tests** with a `FakeEngine: CoreEngine`: `testTickPushesWholeFramesOnly` (2000 mic samples → two 960 pushes, 80 left); `testTickFillsRingToTarget`; `testFlushFromEngineFlushesRing`; `testReportsPlaybackEveryTick`.
- [ ] **Step 2:** FAIL. **Step 3:** implement (VPIO itself is verified live in Task 10). **Step 4:** `swift test` PASS.
- [ ] **Step 5: Commit** — "feat(mac): VoiceProcessingIO audio and pump".

---

### Task 9: Native Swift mascot (renderer, gestures, click-through, proximity fade)  (issue #14)

The mascot is drawn natively; there is no WebView and no `player.js`. The pure logic is a port of ai-mascot's `ui/mascot/player-core.js` (**port its 23 tests from `ui/mascot/test/player-core.test.js` first**). Proximity fade and cursor polling are borrowed from `~/Documents/Projects/oled-saver-macos` (MIT, same author): `CircleKit/Sources/CircleKit/BallPhysics.swift` `proximityOpacity` and the 60 Hz `NSEvent.mouseLocation` poll in `CircleApp/CircleOverlayView.swift`. Keep a one-line attribution comment above each borrowed function.

**Files:**
- Create (package, testable): `Sources/DeskPixieCore/Mascot/{Pack.swift,MascotPlayer.swift,Gesture.swift,SpriteSheet.swift,ProximityFade.swift}`
- Create (app): `mac/DeskPixie/Mascot/{MascotPanel.swift,MascotView.swift,MascotController.swift}`
- Test: `Tests/DeskPixieCoreTests/{MascotPlayerTests.swift,GestureTests.swift,SpriteSheetTests.swift,ProximityFadeTests.swift}`

**Interfaces:**
- Consumes: `CoreEngine.packJSON()` → `{"pack": ResolvedPack, "sheetPath": "/abs/sheet.png"}` (Task 6); `CoreEngine.hotkey()`.
- Produces:
  ```swift
  public struct Pack: Decodable, Sendable, Equatable {            // ResolvedPack JSON (Task 6)
      public struct Bob: Decodable, Sendable, Equatable { public let px: Int; public let frames: Int }
      public struct StateSpec: Decodable, Sendable, Equatable { public let row, frames: Int; public let loop, bob: Bool; public let faceDy: [Int] }
      public struct Mouth: Decodable, Sendable, Equatable { public let row: Int; public let anchor, size: [Int]; public let levels: Int }
      public let name: String; public let cell: [Int]; public let fps: Int; public let bob: Bob
      public let states: [String: StateSpec]; public let mouth: Mouth
  }
  public struct MascotPlayer: Equatable, Sendable {               // port of player-core.js
      public var state: String; public var frame: Int; public var tick: Int
      public init(state: String = "idle")
      public func setting(_ name: String, pack: Pack) -> MascotPlayer   // setState: unknown or same state → unchanged
      public func stepped(pack: Pack) -> MascotPlayer                   // step: non-loop end → idle
      public func bobOffset(pack: Pack) -> Int
      public func bodyRect(pack: Pack) -> CGRect                        // in sheet pixels, top-left origin
      public static func clampLevel(_ level: Int, pack: Pack) -> Int
      public static func mouthRect(level: Int, pack: Pack) -> CGRect
      public func mouthPos(pack: Pack) -> CGPoint
  }
  public enum GestureResult: Equatable { case none, press, drag, wait, double, click }
  public struct Gesture {                                           // port of gesture(); times in seconds
      public init(slop: CGFloat = 3, double: TimeInterval = 0.3)
      public mutating func down(at: CGPoint, time: TimeInterval, leftButton: Bool) -> GestureResult
      public mutating func move(to: CGPoint) -> GestureResult
      public mutating func up(at: CGPoint, time: TimeInterval, leftButton: Bool) -> GestureResult
      public mutating func expire(now: TimeInterval) -> GestureResult
  }
  public final class SpriteSheet: @unchecked Sendable {
      public init(image: CGImage)                                   // keeps RGBA bytes for hit tests
      public convenience init?(url: URL)
      public func crop(_ r: CGRect) -> CGImage?                     // cached per rect
      public func isOpaque(sheetPoint: CGPoint) -> Bool             // alpha > 0 at that sheet pixel
  }
  public enum ProximityFade {
      // borrowed from oled-saver-macos BallPhysics.proximityOpacity (MIT)
      public static func opacity(spriteFrame: CGRect, cursor: CGPoint, fadeRadius: CGFloat, enabled: Bool) -> CGFloat
      // distance is from the cursor to the nearest point of spriteFrame (0 inside); 1.0 at >= fadeRadius,
      // 0.0 at <= 30% of fadeRadius, quadratic in between. Floor the result at `minOpacity` = 0.15 so Bit never fully vanishes.
  }
  ```

**MascotView** (layer-backed `NSView`, `wantsLayer = true`, clear background): two `CALayer`s, body and mouth, both `magnificationFilter = .nearest`, contents from `SpriteSheet.crop`. Geometry in cell pixels × `scale` (default 8 → 256×256 for Bit), y flipped (sheet is top-left origin). Per update wrap in `CATransaction.begin(); setDisableActions(true) … commit()` (as oled-saver does).
**MascotController** (`@MainActor`): owns `pack`, `sheet`, `player`, `mouthLevel`, `gesture`.
- Animation: a `Timer` at `1/pack.fps` steps the player and redraws.
- `setState(_:)`, `setMouth(_:)` — called by the coordinator on `state` / `mouth` engine events.
- Cursor poll: a 60 Hz `Timer` reading `NSEvent.mouseLocation` (screen coords, oled-saver's reason: global monitors don't fire while our app is frontmost). Each tick:
  1. **Click-through where empty:** map the cursor into the current *body* cell (account for bob, scale, flip); `panel.ignoresMouseEvents = !(cursor inside panel && sheet.isOpaque(...))`. While a gesture press/drag is active keep `false`.
  2. **Proximity fade:** `view.layer.opacity = Float(ProximityFade.opacity(spriteFrame: panel.frame, cursor:, fadeRadius: 150, enabled: true))`.
- Gestures: `MascotView` forwards `mouseDown/Dragged/Up` (window coords, `event.timestamp`). `drag` → `panel.performDrag(with: event)`; `double` → `engine.hotkey()`; `wait` → schedule `expire` after 0.31 s, `click` → no-op in Phase 3 (terminal is Phase 5), log it.
**MascotPanel:** `NSPanel(styleMask: [.borderless, .nonactivatingPanel])`, `isOpaque = false`, `backgroundColor = .clear`, `hasShadow = false`, `level = .floating`, `collectionBehavior = [.canJoinAllSpaces, .stationary, .fullScreenAuxiliary]`, `isReleasedWhenClosed = false`, `acceptsMouseMovedEvents = true`, `becomesKeyOnlyIfNeeded = true`; initial frame bottom-right of `NSScreen.main!.visibleFrame` with a 24 pt margin; `orderFrontRegardless()`.

- [ ] **Step 1: Port tests first.** Read `git -C ~/Documents/Projects/ai-moscot show origin/main:ui/mascot/test/player-core.test.js`; translate every test into `MascotPlayerTests` / `GestureTests` (same names, same fixtures — the test pack fixture becomes a `Pack` literal decoded from the same JSON). Add:
  - `ProximityFadeTests`: cursor inside the frame → 0.15 (floor); far (≥ radius from the edge) → 1.0; disabled → 1.0; at 65% of radius → `((0.65-0.3)/0.7)^2` = 0.25 (± 1e-6); monotonic over 0…radius.
  - `SpriteSheetTests`: build a 4×2 `CGImage` in-test (one opaque pixel at (1,0)); `isOpaque((1,0))` true, `(0,0)` false, out of bounds false; `crop` returns the right size.
  - `PackDecodingTests`: decode `{"pack":…}` from Task 6's real output for `mascots/bit` (`git show`-free: load `../../../mascots/bit/mascot.json` through a tiny JSON fixture copied into `Tests/Fixtures/bit-pack.json` matching the ResolvedPack shape).
- [ ] **Step 2:** `swift test` → FAIL. **Step 3:** implement the package files. **Step 4:** PASS.
- [ ] **Step 5:** implement `MascotPanel`, `MascotView`, `MascotController` (verified live in Task 10 Step 4).
- [ ] **Step 6: Commit** `mac/DeskPixie/Mascot mac/Packages/DeskPixieCore` — "feat(mac): native mascot renderer with click-through and proximity fade".

---

### Task 10: App shell — xcodegen project, tray, hotkey, coordinator, live run  (issue #15)

**Files:**
- Create: `mac/project.yml`, `mac/Makefile`, `mac/Configs/SharedSettings.xcconfig`, `mac/DeskPixie/{DeskPixieApp.swift,AppDelegate.swift,Info.plist,DeskPixie.entitlements}`, `mac/DeskPixie/Composition/CompositionRoot.swift`, `mac/DeskPixie/Engine/EngineCoordinator.swift`, `mac/DeskPixie/MenuBar/MenuBarMenu.swift`, `Sources/DeskPixieCore/Hotkey/CarbonHotkey.swift`, `Sources/DeskPixieCore/Secrets/EnvSecrets.swift`
- Test: `Tests/DeskPixieCoreTests/EnvSecretsTests.swift`

**project.yml** — mirror Howl's `mac/project.yml` (read it): one app target `DeskPixie`, `PRODUCT_BUNDLE_IDENTIFIER: com.sihekuang.deskpixie`, `PRODUCT_NAME: DeskPixie`, deployment 14.0, Swift 6, strict concurrency complete, arm64, hardened runtime, `LD_RUNPATH_SEARCH_PATHS: "@executable_path/../Frameworks"`, `LIBRARY_SEARCH_PATHS: "$(SRCROOT)/../core/build"`, `OTHER_LDFLAGS: "-ldeskpixie"`, local package `DeskPixieCore`. preBuildScript "Build Go core" (`source ~/.zprofile 2>/dev/null; export PATH=/opt/homebrew/bin:$PATH; cd "$SRCROOT/../core" && make build-dylib`, `basedOnDependencyAnalysis: false`); postCompileScript copies `libdeskpixie.dylib` into `$BUILT_PRODUCTS_DIR/$FRAMEWORKS_FOLDER_PATH`. Resources: folder reference `../mascots` → `mascots`. xcconfig: ad-hoc signing (`CODE_SIGN_IDENTITY = -`), `#include? "DeveloperSettings.xcconfig"`.
**Info.plist:** `LSUIElement` true, `NSMicrophoneUsageDescription` "DeskPixie listens when you start a conversation with Bit.", `CFBundleName` DeskPixie. **Entitlements:** app-sandbox false, `com.apple.security.device.audio-input` true, `cs.disable-library-validation` true.
**mac/Makefile:** `project` (xcodegen generate), `build` (`xcodebuild -project DeskPixie.xcodeproj -scheme DeskPixie -configuration Debug -derivedDataPath build build`), `test` (`cd Packages/DeskPixieCore && swift test`), `run` (`open build/Build/Products/Debug/DeskPixie.app`).

**EnvSecrets:** `static func openAIKey(env: [String:String], dotenvURL: URL) -> String?` — env `OPENAI_API_KEY` wins; else parse `KEY=value` lines (ignore `#`, blank, `export ` prefix, surrounding quotes). Tests: env wins; dotenv parsed; quotes stripped; missing → nil. Never log the value.

**CarbonHotkey:** port Howl's `CarbonHotkeyMonitor` key+modifier path only (`RegisterEventHotKey`), default `kVK_Space` + `controlKey | optionKey`, callback on press.

**CompositionRoot** (`@MainActor`): `engine = LibDeskPixieEngine()`, `voiceIO = VPIOVoiceIO()`, `ring = PlaybackRing(capacity: 24000*120)`, `micRing = SampleRing(capacity: 48000)`, `pump`, `hotkey`, `mascot = MascotController(engine:, panel: MascotPanel())`, `coordinator`.
**EngineCoordinator** (`@MainActor`): `start()` → `engine.configure(EngineConfig(apiKey: EnvSecrets…, packRoots: [Bundle.main.resourceURL!/mascots, AppSupport/com.sihekuang.deskpixie/mascots]))`; on error, set `status` and show `error` state. Register the hotkey → `engine.hotkey()`. Start `voiceIO` (mic disabled) and `pump`. 30 ms poll loop (Howl's pattern) handling events: `state` → `mascotController.setState` + store for tray; `mouth` → `mascotController.setMouth`; `mic(start:)` → `voiceIO.setMicEnabled`; `status`/`error` → tray status line; `metrics` → `os_log`. `voiceIO.start` throwing `.permissionDenied` → `engine.micFailed("Microphone access denied")`.
**MenuBarMenu** (SwiftUI `MenuBarExtra`, `.menu` style): status line (disabled Text), "Talk to Bit (⌃⌥Space)", State ▸ the six states (debug: calls `mascotController.setState` directly), "Quit DeskPixie".

- [ ] **Step 1: Failing tests** for `EnvSecrets` → FAIL → implement → PASS (`make -C mac test`).
- [ ] **Step 2:** Write project.yml, plist, entitlements, xcconfig, Makefile, app sources.
- [ ] **Step 3:** `make -C mac project build` → `** BUILD SUCCEEDED **`. Fix until it does.
- [ ] **Step 4: Smoke run:** `make -C mac run`; confirm Bit appears bottom-right, idle animation plays, tray icon present, State ▸ listening changes the sprite. (Without a key: hotkey → error state + tray "OPENAI_API_KEY not set".)
- [ ] **Step 5: Commit** `mac core/Makefile` — "feat(mac): DeskPixie app shell with tray, hotkey and coordinator".
- [ ] **Step 6: Live voice check (needs the user's key, done by the user):** set `OPENAI_API_KEY` in `~/Library/Application Support/com.sihekuang.deskpixie/.env`; ⌃⌥Space; say "hi Bit, what's two plus two"; Bit answers with mouth movement; interrupt Bit mid-sentence → it stops within a beat; stay silent 7 s → conversation ends.

---

### Task 11: Go/no-go measurement  (issue #16)

**Files:**
- Create: `docs/go-no-go.md`

- [ ] **Step 1:** Write `docs/go-no-go.md`: the procedure (same machine, same mic, 10-minute conversation in each app, barge-in ×10), what is read from where (DeskPixie: `~/Library/Logs/DeskPixie/metrics.log` last snapshot; Rust app: its mic→send log lines and underrun counters — find them with `git grep -n "underrun\|latency" origin/main -- crates src-tauri` and cite them), a results table (mic→send p50/p95 ms, underruns, max GC pause ms, barge-in stop time), and the rule: **go** if DeskPixie's p95 mic→send ≤ Rust's + 10 ms, underruns ≤ Rust's, and max GC pause < 5 ms; otherwise **no-go**, which triggers D2 (stop at the middle option).
- [ ] **Step 2:** Commit; post the filled-in table on the Phase 3 epic issue after the user's live session.

---

## Deferred to Phase 4 (create as issues)
- Mascot position persistence + size menu (#17).
- Settings window (SwiftUI from Howl), Keychain secrets, first-run, data migration (§6.2).
- Gemini adapter, local tools, chimes, device pinning, `deskpixie-cli` headless harness.
