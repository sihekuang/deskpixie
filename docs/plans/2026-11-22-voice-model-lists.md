# Voice models from the provider API (#477) — design and plan

Builds on #472 (connect errors logged) and #474 (GPT-Live uses its own model/voice), on branch
`voice-model-lists`; one PR carries all three.

## Problem

The voice Model choices are hard-coded (`core/settings/schema.go`), and one `voice.model` is
shared by every provider. New versions need a code change (OpenAI already lists `gpt-live-1.1`
and `gpt-realtime-2.1`, which the code doesn't know), and switching provider can leave the old
provider's model behind: GPT-Live got `gpt-realtime` and every connect failed (#473).

## Design

1. **The family, in code, per adapter** (`core/voice/<provider>/models.go`). Each adapter says
   which ids are its models and its default. Checked live against the APIs on 2026-11-22:
   - GPT-Live (`openailive`): `gpt-live-…`, not `…-transcribe`. Default `gpt-live-1`.
   - OpenAI Realtime (`openai`): `gpt-realtime…`, not `…-translate`, `…-whisper`,
     `…-transcribe`. Default `gpt-realtime`.
   - Gemini Live (`gemini`): `gemini-…` with `live` or `native-audio` in the id and
     `bidiGenerateContent` in `supportedGenerationMethods`, not `transcribe` / `translate`;
     the `models/` prefix is stripped. Default `gemini-3.8-live`.
   The same rule is what each adapter's Resolve checks before connecting: a model outside the
   family is never sent; the default is used and the replacement logged once (#474's rule, now
   for all three; `openai` gains it).
2. **The versions, from the API.** A voice.Model may implement
   `voice.ModelCatalog { ListModels(ctx, apiKey) ([]string, error) }`:
   - OpenAI and GPT-Live: `GET {base}/v1/models` with `Authorization: Bearer`.
   - Gemini: `GET {base}/v1beta/models?pageSize=1000` with `x-goog-api-key` (header, never the
     URL), following `nextPageToken`.
   Each filters by its family, sorts with the default first then the rest by name, and never
   puts the key in an error, the log or a URL. Base URLs are injectable for tests.
3. **The cache** (`core/voice/modelcache.go` or a small package): `<app data>/voice/models.json`
   `{provider: {"models": [...], "fetched": time, "error": "short text"}}`, atomic write; a
   damaged or unreadable file is set aside as `.bad` and the cache starts empty. A `Refresher`
   refreshes one provider in the background: at most one fetch in flight per provider, not
   again within an hour after a success (a failure may retry after a minute), key from the
   voice secrets (none: no fetch, note "no API key"). It calls `onChange()` when a provider's
   list or error changed.
4. **A model per provider** (settings). `voice.model` is replaced by `voice.openaiModel`,
   `voice.geminiModel`, `voice.liveModel`, each `visibleWhen` its provider (as
   `voice.liveBackendModel`), an open choice from `SourceVoiceModels` with ApplyRestart.
   Defaults from `ProviderDefaults`. Migration (like `migrateListenAfterUpdates`): a
   settings.json with `voice.model` and none of the new keys puts it in the field whose family
   it matches by prefix (`gpt-live` → live, `gpt-realtime` → openai, `gemini-` / `models/gemini-`
   → gemini), else the current provider's field; the others keep their defaults.
   `swapProviderDefaults` no longer touches the model (the voice swap stays).
5. **Settings shows the loaded list.** `OptionSources` gains `VoiceModels map[string][]string`
   and `VoiceModelsNote map[string]string`. `SourceVoiceModels` shows the provider's loaded
   list (labels = the id), falling back to the built-in list; the saved value is always in the
   list (`WithSaved`-style). When the last refresh failed, the model field's help gets one plain
   line: "Couldn't load the list from OpenAI (<short reason>); showing the saved one." No Swift
   change: the window is drawn from the schema.
6. **App wiring** (`core/app`). `SessionConfig` takes the current provider's model field.
   `settingsEnv` fills `VoiceModels`/`VoiceModelsNote` from the cache. A settings snapshot and a
   `voice.provider` change start a refresh of the current provider; `onChange` emits
   `settings.changed` so an open window redraws.

Not in scope: voices (neither API lists them).

## Tests

- Family rules against fixtures of the real lists (ids only, no keys) — including the
  non-conversational ones excluded.
- Listers against `httptest` servers: auth header sent, key absent from URL and errors, paging
  (Gemini), HTTP errors and bad JSON give a short error.
- Cache: round trip, damaged file set aside, unwritable folder kept in memory, rate limit,
  one in flight, no key.
- Settings: migration cases (each family, unknown, already migrated), visibility, options from
  the loaded list with the saved value kept, the note in help, swap no longer moves the model.
- App: SessionConfig per provider; a refresh emits settings.changed.
- All with `t.TempDir()`, no real home, settings, keys or network.

## Phases

- **A** — voice: families + Resolve for all three adapters, `voice.ModelCatalog` listers.
- **B** — the cache and Refresher.
- **C** — settings: three fields, migration, options + note, swap change, docs/search entries.
- **D** — app wiring, `settings.changed` on refresh, docs (`docs/` mentions of `voice.model`),
  then an isolated test build checked by hand.

## As built

Where the build differs from the design above, or adds to it:

- **Lists.** The shared code is `voice.GetJSON` and `voice.OrderModels` (`core/voice/modellist.go`).
  An error is short ("openai models: http 401: …"). The key is blanked out before the message is
  clipped, and the URL is never in it. Gemini's list is an error, not part of a list, when a page
  token repeats or it runs past 20 pages. The REST base URL is a field next to the WebSocket one.
- **Cache.** A failed load keeps the models from before and records only the error. With no key
  the error is "no API key" and nothing is fetched. `Refresher.RefreshNow` skips the hour and
  minute waits; the app calls it when a provider's key is saved or removed. `Close` cancels the
  loads. `OnChange` always runs on its own goroutine, so a caller holding a lock it takes can't
  deadlock.
- **Settings.** `OptionSources.VoiceModelsSaved` (filled from the config, like `VoiceProvider`)
  keeps each saved model among its choices. A loaded id that the built-in list knows keeps its
  label ("GPT Realtime mini"); any other id is its own label. The help line is "Couldn't load the
  list from OpenAI|Google (<reason>); showing the last loaded list." when a list loaded before,
  else "…; showing the built-in list." The reason is clipped to 80 characters. A `voice.model`
  that matches no family, with a provider this version doesn't know, is dropped and the drop is
  logged.
- **App.** `Deps.ModelCatalog` replaces the adapters' lists in tests, so they never reach the
  network. `settings.snapshot` and `settings.reload` refresh before they answer, outside the
  settings lock. The adapters' "replaced" log lines name the new settings (`voice.openaiModel`, …).
