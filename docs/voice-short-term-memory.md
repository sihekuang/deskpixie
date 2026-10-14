# Bit's short-term memory

Bit remembers recent conversations so the owner doesn't have to re-explain context, even
after the app restarts.

- **Setting:** `voice.shortTermMemory` (Settings ▸ Voice ▸ "Remember recent conversations"),
  default **on**, applies on restart. Off: nothing is recorded or injected (an existing file
  is left alone and expires on its own).
- **Storage:** `~/Library/Application Support/com.sihekuang.deskpixie/voice-memory.json`
  (mode 0600, written atomically). Local only; nothing is sent anywhere except as part of the
  session instructions to the voice provider Bit already talks to.
- **What is kept** (`core/voice/memory.go`): the user's transcribed words, Bit's spoken
  replies, requests handed to the coordinator (`delegate` / `answer_job`, with the
  `[ref job_…]` id or the error), and coordinator lines actually given to Bit (`Agent reply`,
  `Agent question`, `Agent says`, `Agent replies while you were away`; not progress narration
  or timer notices).
- **Limits:** entries older than `MemoryWindow` (6 h) expire; at most `MemoryMaxEntries` (60);
  each entry cut to `MemoryEntryChars` (400).
- **Injection:** on every connect (first start and each reconnect) the memory is appended to
  the instructions as a "Recent conversations" block through the runtime's `SessionFacts`
  hook, so it works the same for every provider. The block is capped at `MemoryBudgetChars`
  (3000 characters, ~750 tokens), oldest entries dropped first. `persona.md` tells Bit to use it
  as background and not recite it.
- **Clearing:** say "forget our recent conversations" (the `forget_recent` voice tool; the rest
  of that conversation isn't recorded either), the `memory:forget` app action
  (`app.action`, no menu item yet), `App.ForgetMemory()`, or delete the file.
