# Parity inventory: voice + app (ai-mascot origin/main, excluding delegation crates)

Read the Rust source with `git -C ~/Documents/Projects/ai-moscot show origin/main:<path>`. Counts are `#[test]`s (JS: `test(`).

| Area | Lines | Tests | DeskPixie home |
|---|---|---|---|
| crates/voice-core (beyond conversation.rs) | 3,049 | 76 | Go `voice` |
| crates/voice-gemini | 2,677 | 60 | Go `voice/gemini` |
| crates/voice-openai | 1,254 | 28 | Go `voice/openai` (done in Phase 3) |
| crates/voice-tools | 2,038 | 58 | Go `tools` |
| crates/canvas-core | 2,089 | 68 | Go `canvas` |
| crates/mascot-pack (runtime parts: manifest, validate, resolve, hit_mask) | ~800 | 41 | Go `packs` (generators are build-time only: not ported) |
| crates/audio-macos | 3,067 | 76 | Swift (VPIO, devices, watch, mic mode, permission) + Go `audio` (denoise) |
| src-tauri/src | 10,765 | 271 | Go `config`/`settings`/`app` + Swift shell |
| ui/settings | 1,122 | 27 JS | SwiftUI settings |
| ui/mascot | 476 | 23 JS | Swift (done in Phase 3) |

## voice-core
- Runtime 33 ms tick. `VoiceHandle`: `hotkey()`, `inject(Notice)` (spoken once nobody talks; when closed, returned in `Closed{undelivered}`), `hold_open(bool)` (silence timeout → hold timeout, rest in `delegating`), `set_mouth_levels`, `is_open`, `subscribe()` (signals Opened, Closed{undelivered}, UserSaid(text)), `shutdown`.
- Tools: `ToolRegistry` name-keyed, specs sorted by name, each call its own task, results tagged with a generation (stale dropped), unknown → `{"error":"unknown tool X"}`. `end_conversation` (no params, `{"ok":true}`, sends result with respond:false, ends after the reply plays).
- `SessionFacts` appended after a blank line on every connect.
- Chimes (earcons.rs): synthesized, two 90 ms notes, 10 ms fade, amp 0.25. Start E5 659.25 → A5 880; end A5 880 → D5 587.33. Only when `voice.chimes`.
- Failure status lines: "Voice: no API key (set VAR)", "Voice: microphone access denied", "Voice: no answer to the microphone prompt", "Voice: connection lost", "Voice: model error", "Voice: audio device changed", "Voice: <setup msg>".
- env_secrets: openai-realtime → OPENAI_API_KEY, gemini-live → GEMINI_API_KEY; env first then .env paths (cwd, app data); BOM, `export `, `#`, quotes; `key_hint`; key vars scrubbed from child envs.
- `VOICE_DEBUG`: recording.rs writes `<appdata>/recordings/mic-<epoch ms>[-n].wav` (+ `-original.wav` pre-filter), header refreshed every 50 frames, own thread; transcript logging.
- conformance.rs: suite every VoiceModel adapter passes (run against fakes).

## voice-gemini
- `wss://generativelanguage.googleapis.com/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent?key=KEY`; key scrubbed from all errors.
- Timeouts: connect 10 s, write 5 s, ping 10 s, idle 30 s, poll 200 ms. Default model `gemini-3.8-live`, voice `Puck` (30 prebuilt voices; non-`gemini-` model / unknown voice substituted, logged once).
- Setup: AUDIO modality, prebuilt voice, input+output transcription, `START_OF_ACTIVITY_INTERRUPTS`, noisy room → `START_SENSITIVITY_LOW`, manual → detection disabled, systemInstruction when non-empty, tools `behavior: BLOCKING`.
- Input 16 kHz, output 24 kHz; streaming linear-interp resampler.
- Derived events: ResponseCreated/Done, SpeechStarted/Stopped; `USER_QUIET` 2 s, `USER_QUIET_WITH_VAD` 15 s, `LATE_TRANSCRIPT_GRACE` 1 s. Cancel closes reply locally; respond:false drops reply until user speaks. Transcripts joined per response. goAway/toolCallCancellation warnings; close 1007/1008 fatal.
- schema.rs: JSON Schema → Gemini OpenAPI subset. `GEMINI_TRACE=1`.

## voice-tools (LOOKUP_TIMEOUT 3 s → `{"error":"timeout"}`)
- `get_time{place?}` (Open-Meteo geocoding; US abbreviations ET/CT/MT/PT/AK/HT…), `get_weather{location?}` (default `voice.homeCity`; Open-Meteo forecast; °F/mph in US; labels), `calculate{expression | value,from,to}` (evalexpr semantics: floats, ≤200 chars, whitelist, functions, 6 significant digits, unit conversions), `timer{action start|cancel|list, duration_seconds 1..86400, label?}` (MAX 10; label matching; "Timer done: <label> (<spoken length>)."), `media{action play|pause|next|previous|volume|now_playing, level}` (osascript; Spotify then Music; pgrep; 5 s; 300 ms after skip), `LocalFacts` (date/time, zone, home city, routing rules).

## canvas-core
- CSP: `default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'none'; frame-src 'none'; form-action 'none'; base-uri 'none'`.
- Slug `[a-z0-9-]{1,64}`, `/pages/<slug>.html`, MAX_PAGE_BYTES 5 MB, title from `<title>` in first 64 KB (≤120 chars, entities decoded). Resolver: `/ds/**` and `/pages/<slug>.html` only, refuses traversal in all spellings, dotfiles, empty segments, escaping symlinks; content types by extension.
- PollingSource (mtime,len); Controller: autoOpen default true, debounce 300 ms, recent 10, duplicate hash suppressed; outcomes Shown (Bit happy)/Updated/Lint/Rejected; `open_last`, `open(id)`; prune(keep) at startup.
- Linter warnings list (external URLs, script src outside /ds/, network APIs, meta refresh, form, frames/objects, links to other pages, missing tokens.css, missing title).

## mascot-pack (runtime)
- Contract v1; `loop`/`bob` default true; `faceDy` optional. STATE_NAMES idle, listening, thinking, delegating, happy, error; REQUIRED idle, listening, thinking; fallbacks delegating→thinking, happy→idle, error→idle. Limits MAX_SHEET_SIDE 4096, MAX_SHEET_BYTES 16 MB, MAX_BOB_PX 16, sheet inside pack dir.
- Hit mask = union of every body frame, every mouth level at every faceDy, plus bob offset.

## audio-macos
- VPIO starts on demand, stops after 300 ms idle; ducking Min; speaker ring 120 s; mic ring 2 s; 40 ms frames; 1 s back-off after failed build.
- Mic permission: AwaitingPermission → Live/PermissionDenied (60 s wait).
- Devices: pinned by CoreAudio UID; VPIO aggregates hidden (`CADefaultDeviceAggregate`, `VPAUAggregateAudioDevice`); "(Bluetooth)" label; missing pinned device → system default; `build_with_fallback`.
- Device watch: ignore changes in 3 s SETTLE after start; change must hold 2 polls (1 s); pinned device must keep its AudioDeviceID; real change ends conversation (AudioDeviceChanged).
- Noise filter (live switch): Hush (DFN3 retrained 16 kHz, hop 160, atten limit 18 dB, embedded model `models/hush16k.tar.gz`), DFN3 (48 kHz hop 480); dither −68 dBFS on near-silence; loads on helper thread, pass-through until ready/on failure.
- Mic Mode: `AVCaptureDevice.showSystemUserInterface(.microphoneModes)` (macOS 12+), tray item.

## src-tauri
- Data: `<app_data>/settings.json`, `<app_data>/mascots/`, pack roots bundled then user.
- config.rs: load never fails (defaults written back); normalize (scale 0→8, cap 16; blank paths → null; homeCity trimmed); atomic save (temp+fsync+rename, keep perms, follow symlink); unknown keys preserved; managed `position`, `canvas.window` survive writes.
- Hotkey: default `ctrl+alt+Space`; rebind with rollback; startup failure tray line "Hotkey unavailable: <combo>"; no voice → toggles listening.
- Proximity loop 60 Hz: hit-mask click-through; fade (1 beyond radius×displayScale, 0 within 30%, quadratic; 1 when over Bit or off); emit on ≥0.01 change or reaching 0/1.
- Position: saved 400 ms after a drag settles; Reset position → null → default (bottom-right of primary, 40 px from right, 80 px from bottom); restore_position keeps a saved position while the window centre is on any display, else clamps into nearest work area.
- Scale change keeps feet (bottom-centre). Pack select saves `pack`; Reload never saves.
- One-shots: happy/error return to idle after (frames+1)×frame time; `celebrate` only from idle; `flash_error` only from idle/delegating, returns to previous state; looping variant holds 2 s.
- Simulate talking: 33 ms fake mouth envelope, silent while a conversation is open.
- Tray (rebuilt after every change), size presets Small ×4/Medium ×6/Large ×8/Huge ×12/Custom (×N); recent titles ≤60 chars; job rows ≤40 chars "<task> — <status>, <age>". Tooltip, left-click opens menu.
- TimerAlarm: during a conversation inject; else (or undelivered) Glass.aiff + macOS notification "Timer done" / "<label> (<length>)".
- Settings: schema FIELDS (Bool, Int{min,max,menu}, Choice, MultiChoice, Text{optional}, Path{dir,optional}, Hotkey, Managed), platforms, Apply Live/Restart. Live: hotkey, pack, scale, proximityFade.enabled, proximityFade.radius, voice.noiseFilter, voice.inputDevice, voice.outputDevice, canvas.autoOpen. Service set_field: merge hand edits → parse → validate (existing problems don't block) → hotkey applied before save ("That shortcut is in use") → atomic save → live apply (warning "<Label> couldn't be applied now (…); it applies after a restart."). reload on focus; broken file banner "settings.json has a syntax error; fix it or reset it" blocks saves. Snapshot {platform, values, fields, restartPending, fileError, warning, seq}. Validation messages: "Use a whole number from N to M", "Not one of the choices", "Not available on this platform", "Not a key combination (e.g. ctrl+alt+Space)", "Choose an existing folder inside your home folder", session `[a-z0-9][a-z0-9-]{0,47}` not "default", "No file at that path", workers need ≥1 kind; device UIDs never rejected. ops: restart, open file (`open -R`), secrets Found/Missing.
- Native settings window: tabs Mascot (face.smiling), Voice (waveform), Agent (sparkles), Workers (person.2), Canvas (rectangle.on.rectangle), Advanced (gearshape.2); remembers frame + last tab; control per kind (switch, popup, field+stepper, checkboxes, text, path control with Choose…/Clear, hotkey recorder: needs ⌃/⌥/⌘ unless F1–F20, Esc cancels, shown with Mac symbols, "Type shortcut…"); "(not available on this platform)", "(not found)"; extra rows Reset Position, OpenAI/Gemini key Found/Missing/Unknown with help "Set OPENAI_API_KEY in your environment or a .env file. Keys are never shown here.", Open Settings File; file-error banner, warning line, Restart bar "Restart to apply: <labels>"; refused input stays with error under it; stale snapshots ignored by seq.
- Canvas window: pages `<app_data>/canvas/pages`, design system bundled `design-system/`; copies CANVAS.md + examples to app data when different; tick 100 ms, poll every 500 ms; prune keep at startup; `canvas://` scheme only for the canvas webview, GET/HEAD, CSP header + meta CSP after doctype, nosniff, no-referrer, no-store; window 960×720 centred or saved geometry (≥100 px overlap, clamped, title bar on screen, min 480×360); opens without focus; title "Canvas — <title>"; empty state `canvas://localhost/ds/empty.html`; WebRTC/mediaDevices removed, permissions denied, navigation pinned to page, no new windows; geometry saved 500 ms after settle and on close/exit. Tray: Open canvas, Recent ▸, Open pages folder.
- Info.plist: NSMicrophoneUsageDescription, NSAppleEventsUsageDescription; entitlements audio-input + automation.apple-events.

## settings.json schema (camelCase, defaults)
```jsonc
{ "hotkey": "ctrl+alt+Space", "pack": "bit", "scale": 8, "position": null,
  "proximityFade": { "enabled": true, "radius": 150 },
  "voice": { "provider": "openai-realtime", "audio": "macos-vpio", "model": "gpt-realtime", "voice": "marin",
             "chimes": true, "idleTimeoutSecs": 7, "personaPath": null, "noiseFilter": false, "noiseModel": "hush",
             "noisyRoom": false, "inputDevice": null, "outputDevice": null, "homeCity": null },
  "canvas": { "autoOpen": true, "keep": 50, "window": null },
  "agent": { "enabled": true, "sessionHost": "herdr", "session": "deskpixie", "terminal": "warp", "herdrPath": null,
             "model": null, "warm": false, "port": 48217, "keepPanesOnQuit": false, "holdOpenSecs": 180,
             "permissionTimeoutSecs": 120, "notifyPerMinute": 4, "narration": true, "projectsRoot": "~/Documents/Projects",
             "workers": { "enabled": false, "max": 4, "kinds": ["claude", "codex"] } } }
```
(`agent.session` default renamed `ai-mascot` → `deskpixie`, spec §6.)

## Tray tree
```
[Hotkey unavailable: <combo>] / [Voice: <status>] / [Agent: <status or hint>]   (disabled, conditional)
State ▸ idle listening thinking delegating happy error
Simulate talking
Size ▸ Small (×4) Medium (×6) Large (×8) Huge (×12) [Custom (×N)]
Reset position
Proximity fade
Mascot ▸ <packs>
Reload packs
Canvas ▸ Open canvas / Recent ▸ <titles> / Open pages folder
[Agent ▸ <job rows> / Cancel job ▸ / Open agent terminal / Restart agent]   (when agent.enabled)
──
[Mic Mode (Voice Isolation)…]
Settings… (⌘,)
──
Quit
```
Gestures: click → agent terminal, double-click → voice hotkey, drag → move.
