# Voice preview in Settings (#480): design

## Problem

The Voice setting (`voice.voice`) is a pop-up of names: 31 GPT-Live voices, 10 OpenAI
Realtime and 30 Gemini Live. There is no way to hear a voice before picking it and restarting.

## What the user sees (layout A)

The Voice row in Settings ▸ Voice becomes an inline list instead of a pop-up:

- A search field above the list. It filters by name and description.
- A scrolling list, about eight rows tall. Each row has:
  - a ▶ button (■ while it plays, a spinner while it loads);
  - the voice's name;
  - its description, when the provider publishes one;
  - a checkmark on the saved voice.
- Clicking a row picks that voice. As today, the change applies after a restart.
- Pressing ▶ plays a short line in that voice, through the speaker Pixie uses: "Hi, I'm Kore.
  This is how I sound." Pressing ■, or ▶ on another voice, stops it.
- Under a row, one plain line when a preview can't play: "Add an OpenAI API key to hear
  previews." or "Couldn't load the preview (no connection)."

The list follows the voice service, as the choices do today. Descriptions:

- **Gemini Live:** Google's one-word descriptors (Kore "Firm", Puck "Upbeat", …). These were
  in the design canvas the user approved.
- **OpenAI (GPT-Live and Realtime):** OpenAI publishes none, so the row shows only the name.
  No descriptions are made up.

## Where the preview audio comes from

It comes from the provider itself, for every provider, through the one interface every adapter
already has: `voice.Model.Connect`. No provider needs new code, and a new provider gets
previews for free.

1. Open a session with the voice service's saved model and the voice being previewed. Use short
   preview instructions ("Say the line you're given, word for word, in a natural tone"), and no
   tools.
2. Wait for `EvReady`. Then send `CmdInject{Text: the line, Respond: true}`.
3. Collect `EvAudioDelta` until `EvResponseDone`. Close the session. Time out after 20 s.
4. Save the audio to `<app data>/voice/previews/<provider>/<model>/<voice>.pcm` (24 kHz mono
   s16le, written atomically). Later plays read the file and cost nothing.

The first play of each voice costs about a cent and takes 2–3 s. GPT-Live needs input audio
before it speaks an injected line. #483's idle silence already supplies it for a session with
no mic, so this builds on #483.

The cache key includes the model, because a voice can sound different on another model. Bumping
a version number in the line text starts a fresh cache. A damaged or empty file is deleted and
made again.

## Playback

Audio (VoiceIO and the pump) runs from launch, and every sound already goes through the Go
playback queue (Pixie's voice, the chimes). A preview uses the same path. That keeps the
speaker setting and needs no new Swift audio code.

- `Runtime.PlayClip(samples) bool` queues the clip only while the runtime is at rest
  (PhaseIdle). Otherwise it returns false and nothing plays over a conversation.
- `Runtime.StopClip()` flushes it.
- A conversation that starts flushes any clip still playing.

## Go ↔ Swift

Settings stays drawn from the schema. Go decides, and Swift only draws and forwards clicks.

- **Schema.**
  - `VoiceDetails` (provider → voice → description) fills each voice option's `Detail`.
    `Opt` keeps its three fields, since it is written as a positional literal everywhere.
  - `Choices` gains `Preview bool`, set on `voice.voice`.
  - `KindView`'s choice JSON gains `"preview": true`, and each option gains `"detail"`.
  - The voice list's own search field matches names and descriptions. Settings search is
    unchanged.
- **Calls.**
  - `voice.preview {voice}` plays that voice for the current provider and its saved model. It
    returns at once with `{state: "loading" | "playing"}`, or with an error:
    - no key;
    - a conversation is open: "Previews play when Pixie isn't talking.".
  - `voice.previewStop` stops the preview.
- **Events.** `voice.preview {voice, state: "loading" | "playing" | "done" | "error", error}`,
  so the row's button follows the preview.
- **Swift.**
  - A choice with `preview` decodes as `SettingsKind.previewChoice`, and options decode
    `detail`. `SettingsLayout` gives it the control `.voices(items, selected)`, which commits
    like `.menu`. The filter and row state logic live in the
    package (`VoicePickerModel`), so they get unit tests.
  - The app target adds `VoicePickerView`: the search field, the list and the rows. Its ▶ calls
    `voice.preview`.

## Limits

- One preview generates at a time. A second ▶ while one is loading cancels the first.
- At most one cached file per provider, model and voice. The files are small (about 150 KB
  each, under 15 MB for every voice of all three providers).
- A preview never touches the saved settings, the conversation or the coordinator.

## Tests

- **Go, voice.**
  - The preview generator against a scripted model: the line is sent, the audio is collected,
    and the cache file is written. Also: the timeout, no `EvReady`, a session error, an empty
    reply, a damaged cache file, an unwritable folder, and a second request cancelling the
    first.
  - `PlayClip` refused during a conversation, and flushed when one starts.
- **Go, settings.** `voice.voice` has `preview` and Gemini details in its view. The
  descriptions match the adapter's voices (a test keeps them in step).
- **Go, app.** `voice.preview` with no key, during a conversation, from the cache, and while
  generating (the events in order). Everything runs in `t.TempDir()`, with no network.
- **Swift package.** Decoding `preview` and `detail`, filtering, and the row state for each
  event.
- **UI test** (offline, in CI): search by description, ▶ with no key says why under the row,
  and clicking a row picks the voice.
- **By hand.** An isolated build: play a voice from each provider, play from the cache, stop a
  preview, ▶ while Pixie talks, no key.
