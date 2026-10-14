# API keys in Settings (Keychain), alongside .env — #59

Status: approved (storage and order confirmed by the user) · 2026-10-14

## Goal
Type the OpenAI and Gemini API keys into the Settings window instead of editing a `.env` file.
`.env` and environment variables keep working.

## Behaviour
- Voice page, the "OpenAI key" and "Gemini key" rows: a secure field with **Save**, and **Remove**
  when a Keychain key exists. The field never shows a stored key.
- The row's status names where the active key comes from: "From environment", "From Keychain",
  "From .env", or "Missing".
- Lookup order for each provider variable (`OPENAI_API_KEY`, `GEMINI_API_KEY`):
  Keychain (the key you typed in Settings) → process environment → `.env` files (as today:
  `./.env`, then AppSupport `.env`). What you see in Settings wins.
- Saving or removing takes effect for the next conversation; no restart.
- Keys are never logged, never written to settings.json, never put in the snapshot.

## Architecture
- **Swift owns the Keychain.** `KeychainSecrets` (DeskPixieCore, Security framework, generic
  password, service "DeskPixie", account = variable name): read / save / delete.
- **Core holds Keychain values in memory.** At startup and after every save/remove Swift calls
  `secrets.keychain {"values": {"OPENAI_API_KEY": "…"}}` (only the keys present). The core's
  secret source becomes a chain: in-memory Keychain values → environment → dotenv files.
- **Status with source.** `settings.secrets` (and the snapshot's `secrets`) keep `openai` /
  `gemini` = "found"/"missing" and add `sources: {openai, gemini}` = "env" | "keychain" |
  "dotenv" | "" (missing).
- **Settings UI** saves via KeychainSecrets, then pushes `secrets.keychain`, then reloads the
  snapshot so the status updates.

## Errors
- Keychain write failure: the row shows "Couldn't save to Keychain" and nothing is pushed.
- Empty / whitespace input: Save is disabled.

## Testing
- Go: chain order (keychain beats env beats dotenv; blank keychain value counts as missing);
  `secrets.keychain` replaces the in-memory set; statuses report the source; values never appear
  in `settings.snapshot`/`settings.secrets` JSON.
- Swift: KeychainSecrets round-trip against a test service name; status text mapping.
- Manual: save a key, start a conversation, remove it, status falls back to .env.
