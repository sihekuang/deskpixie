# Phase 3 go/no-go: DeskPixie (Go + Swift) vs ai-mascot (Rust)

Decides whether the Go core + Swift shell is good enough to continue. A **no-go** triggers D2 (stop at the middle option, see the spec, `docs/specs/2026-10-13-deskpixie-port-design.md` §8).

## Setup (identical for both apps)

- Same Mac, same mic and speakers/headphones, same room, same network, same model (`gpt-realtime`, voice `marin`).
- Run the apps one at a time, never together. Quit the other first.
- Use a fresh launch of each app. Do not change the order between runs of the same session (alternate which app goes first on a second pass if time allows).
- One **10-minute conversation** per app: natural back-and-forth, with Bit talking in long answers (ask for 20-30 s explanations) so the playback buffer fills.
- **Barge-in x10** in each run: while Bit is mid-answer with several seconds queued, start speaking over it. Space them out over the 10 minutes.

## What each app actually measures today

### DeskPixie (instrumented)
`core/metrics` writes a snapshot every 10 s to `~/Library/Logs/DeskPixie/metrics.log`, one line per snapshot:
`<RFC3339> {"micToSendP50Ms":..,"micToSendP95Ms":..,"micSamples":..,"maxGcPauseMs":..,"underruns":..,"micDrops":..}`

- Read the **last line** after the 10 minutes. `underruns` and `maxGcPauseMs` are cumulative since process start.
- `micToSend*` are nearest-rank percentiles over the **last 1024 sends (~41 s)**, not the whole run. So also take the **max of `micToSendP95Ms` over all lines** and record both (`tail` for the last window, `jq` over the file for the worst window).
- `micDrops` must be 0. Any drops mean the Go side lagged behind Swift and invalidate the latency number.

### ai-mascot (NOT instrumented)
Checked with `git grep -i "underrun\|latency\|played\|elapsed" origin/main -- crates src-tauri`:

- **No mic-to-send latency measurement exists.** `crates/audio-macos/src/mic_pump.rs` hands each frame to `mic_state.send(CaptureEvent::Frame)` (engine.rs:223) and `crates/voice-core/src/runtime.rs:219` forwards it as `VoiceCommand::Audio`. Nothing is timed.
- **No underrun counter exists.** `crates/audio-macos/src/speaker.rs` `render()` zero-fills the tail (`out[copied..].fill(0)`) when the ring is short, silently. `counters.rs` only tracks `written/read/played/flush_to/level`.
- Only `played_samples()` exists (used for the `audio_end_ms` truncate, `crates/voice-openai/src/protocol.rs:184`). `eprintln!("[voice] ...")` is the only logging style (no `tracing`).
- There is no GC pause number (not applicable to Rust). Record "n/a"; the GC criterion applies to DeskPixie only.

The Rust numbers therefore need **a throwaway instrumentation patch**, applied on a scratch branch of ai-mascot (do not merge it, do not touch the main checkout):

1. **Mic to send**, `crates/audio-macos/src/mic_pump.rs` and `crates/voice-core/src/runtime.rs`. Stamp the capture time on each 40 ms frame when the VPIO input callback hands it to the pump (use `Instant::now()` at the point the 960-sample frame is complete, the same moment DeskPixie's Swift side stamps `capturedUnixNanos`), carry it alongside `AudioFrame`, and at `runtime.rs:219` (just before `s.commands.send(VoiceCommand::Audio(frame))`) compute `stamp.elapsed()`. Push the value into a 1024-sample ring and every 10 s `eprintln!("[metrics] micToSendP50Ms=.. micToSendP95Ms=.. n=..")` using the same nearest-rank percentile. If carrying the stamp through `AudioFrame` is too invasive, a parallel `VecDeque<Instant>` filled in `mic_pump.rs` and popped at `runtime.rs:219` is equivalent as long as frames are not dropped in between.
2. **Underruns**, `crates/audio-macos/src/speaker.rs` `render()`. Add an `underruns: AtomicU64` to `Counters`; after the copy, `if copied < out.len() && !counters.is_idle_before_this_read() { counters.on_underrun() }`, i.e. count a render callback that had to zero-fill **while a response was still streaming** (written > read at entry, or a response is active). Do not count the normal silence between responses. Log it in the same 10 s `[metrics]` line (`underruns=..`). Use the same definition DeskPixie uses in `dp_report_playback` (`PlaybackRing`: a render callback that zero-filled while `expectingAudio` was set, i.e. the engine returned playback samples within the last ~200 ms), and note any difference in the results.
3. Run the Rust app from a terminal (`cargo tauri dev` or the built binary) and capture stderr: `... 2> rust-run.log`.

## Barge-in stop time (both apps)

The target is playback silent within ~50 ms of the user speaking. Log timestamps alone do not capture the speaker-side delay, so measure with a recording:

- **Method (primary, same for both apps):** record system audio + mic on one timeline (QuickTime screen recording with a loopback input, or any DAW recording the mic and the speaker output together; a phone filming the room with the Mac speaker audible also works as a coarse check). Open it in an audio editor, find the onset of the user's voice and the last audible Bit sample, and take the difference. Do this for all 10 barge-ins; report the median and the worst.
- **Cross-check (DeskPixie):** the Swift audio pump logs each flush (`os_log`, subsystem `com.sihekuang.deskpixie`, category `audio`), and the truncate `audio_end_ms` should equal what was *played* (queued audio excluded). Spot-check that `audio_end_ms` is far smaller than the total audio received for that item.
- **Cross-check (Rust):** `runtime.rs:193/307/312` call `playback.flush()`; with the instrumentation patch, also `eprintln!` an `Instant` at the `flush()` call and compare with the recording.

## Results (fill in after the live session)

| Metric | ai-mascot (Rust) | DeskPixie | Pass rule |
|---|---|---|---|
| mic to send p50 (ms), last window | | | informational |
| mic to send p95 (ms), last window | | | DeskPixie <= Rust + 10 |
| mic to send p95 (ms), worst window | | | informational |
| underruns over 10 min | | | DeskPixie <= Rust |
| max GC pause (ms) | n/a | | < 5 |
| mic drops | n/a | | 0 |
| barge-in stop, median / worst (ms) | | | <= ~50, not worse than Rust |

Note date, macOS version, mic/output devices, and Rust commit (plus the patch hash) next to the table.

## Decision rule

**Go** if all hold: DeskPixie p95 mic to send <= Rust p95 + 10 ms, underruns <= Rust's, max GC pause < 5 ms, and mic drops = 0. Barge-in stop time is judged by ear and the recording; a clearly slower barge-in than Rust is also a no-go.

**No-go** otherwise: trigger D2 (stop at the middle option). Post the filled-in table on the Phase 3 epic issue after the user's live session either way.
