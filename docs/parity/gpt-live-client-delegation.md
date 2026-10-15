# GPT-Live client delegation: live probe (#70)

Date: 2026-10-14. Branch `live-client-delegation` (experiment, not for merge).
Probe: `core/voice/openailive/probe_live_test.go`, raw WebSocket (no adapter), real `gpt-live-1`:

```
PROBE_GPTLIVE=1 PROBE_OUT=/tmp/probe.jsonl go test -tags live -run TestLiveProbeClientDelegation -v -timeout 10m ./voice/openailive/
```

Session: `delegation: {type: "client"}`, a short Bit prompt that says to delegate work, live info, timers and music and to answer the time from context; input = `say` speech converted with `afconvert` to 24 kHz PCM16, 40 ms frames in real time, silence in between. One session, 88 s billed (~$0.07).

## Findings

| Question | Answer (measured) |
|---|---|
| What does `session.delegation.created` contain? | `{"type":"session.delegation.created","delegation":{"id":"item_…","target":"client","type":"delegation"},"offset_ms":3800,"event_id":"event_…"}`. **No task text, no transcript, no reason.** `delegation.type` is `"delegation"` (not `client`); `target` is `"client"`. The id has the `item_` prefix. |
| When does it arrive? | 630–750 ms after the end of the user's audio (3 delegations: 744, 631, 720 ms). It arrives in the same read as the **last input-transcript fragment** of the turn, so the user's words are complete at that moment (the adapter's own user-turn close, 800 ms of quiet later, is too late; the adapter now closes the turn on the delegation). |
| Does GPT-Live speak while waiting? | Yes, at once: "Checking now." / "Sure, checking that." / "Checking." starts 170–490 ms after the delegation event (first loud audio ~375 ms after it). With no answer it then stays silent: an unanswered delegation was silent for 15 s, no error, no timeout event. |
| `session.commentary.append` with the delegation id | Accepted (`session.commentary.appended` ~650 ms later, when the timeline reaches it) and **spoken**, paraphrased: "Handed to the agent as job 7. Tell the user…" → "Okay, the agent's on it; I'll let you know." (a second utterance after "Checking now."). |
| `session.thinking.append` with the delegation id | Accepted and, as the answer to the pending question, **spoken**: weather facts sent as thinking → "It's 17 degrees with light rain and a 12 km/h wind." So thinking is "not spoken on arrival" only when nothing is waiting for it. |
| Commentary 30–90 s later with the old delegation id | Works: sent 36 s after the delegation (and after two other turns), accepted and spoken ("Seems like the TestTmuxSessions test in core/tmux timed out, and that's probably a flaky one."). |
| Commentary later with `delegation_id: null` | Works the same (spoken). |
| Unknown delegation id | `error` `{"type":"invalid_request_error","code":null,"param":"delegation_id","message":"Unknown client or Decision delegation.","client_event_id":"p_…"}`; the session stays open. |
| Time from quiet context | A `session.thinking.append` (`delegation_id: null`) "Local time now: Wednesday, 14 October 2026, 21:16 (PDT, UTC-07:00)." sent before the first turn → "What time is it?" answered "It's 9:16 PM." with **no delegation**. |
| General knowledge / small talk / goodbye | "What is the capital of Australia?" → "Canberra." (no delegation). "Thanks, that's all. Bye!" → "You got it. Take care." (no delegation, no end signal of any kind: the app must end the session itself). |
| Other errors | None besides the unknown-id one. `session.usage.updated` every 15 s (`usage.seconds`, `context_window.usage_ratio`); `session.closed` `close_requested`. |

## Consequences for the build (Phase 2)

- The task must come from the transcript: the adapter closes the user's turn when the delegation arrives (all fragments are in), and the conversation builds the task from those words plus up to 6 earlier lines (the #71 hand-off builder).
- GPT-Live already says "checking" by itself, so the app's acknowledgement is a second line. Work tasks get **commentary** ("handed to the agent as job_…; say you passed it on"), fact checks get **thinking** (the answer comes later as an "Agent reply:").
- Agent replies can go in later as commentary with `delegation_id: null` (unchanged from the backend path); tying them to the old delegation id is possible but not needed.
- `end_conversation` doesn't exist: a transcript goodbye check ends the session.
- The time is pushed as thinking at session start and once a minute.

## Timeline (condensed)

`>` sent, `<` received, `#` probe note, `USER`/`BIT` joined transcript fragments; times in ms from connect.

```
   1503 > session.thinking.append  delegation_id=null  "Local time now: Wednesday, 14 October 2026, 21:16 (PDT, UTC-07:00)."
   2504 # say "Hey Bit, can you check why my build failed?" (2724 ms)
   5225 # speech end
   5969 USER: ' Hey Bit, can you check why my build failed'
   5969 < session.delegation.created {"delegation": {"id": "item_EZ7B0…", "target": "client", "type": "delegation"}, "offset_ms": 3800}
   6140 BIT: ' Checking now.'                     (first loud audio 6344)
   7490 > session.commentary.append  delegation_id=item_EZ7B0…  "Handed to the agent as job 7. Tell the user in a few words…"
   8146 < session.commentary.appended
   7957 BIT: ' Okay, the agent's on it; I'll let you know.'
  15493 # say "What's the weather like in Paris right now?"
  17824 # speech end
  18456 < session.delegation.created {"id": "item_EZ7BC…", "target": "client"}
  18945 BIT: ' Sure, checking that.'
  19964 > session.thinking.append  delegation_id=item_EZ7BC…  "Weather in Paris now: 17 degrees Celsius, light rain, wind 12 km/h."
  20736 < session.thinking.appended
  20717 BIT: ' It's 17 degrees with light rain and a 12 km/h wind.'
  28984 # speech end ("What time is it?")
  29949 BIT: ' It's 9:16 PM.'                       (no delegation)
  36904 # speech end ("What is the capital of Australia?")
  37734 BIT: ' Canberra.'                           (no delegation)
  41969 > session.commentary.append  delegation_id=item_EZ7B0… (36 s old)  "Agent reply: the build failed because TestTmuxSessions timed out…"
  42746 BIT: ' Seems like the TestTmuxSessions test in core/tmux timed out, and that's probably a flaky one.'
  50971 > session.commentary.append  delegation_id=null  "Agent reply: job 7 also found the lint step passes…"
  51548 BIT: ' The lint step is fine, only that one test failed.'
  59973 > session.thinking.append  delegation_id="dlg_doesnotexist"
  59989 < error {"code": null, "message": "Unknown client or Decision delegation.", "param": "delegation_id", "type": "invalid_request_error"}
  65225 # speech end ("Can you look at my calendar for tomorrow?")
  65945 < session.delegation.created {"id": "item_EZ7By…", "target": "client"}
  66151 BIT: ' Checking.'                            (then silent 15 s: nothing sent back)
  83184 # speech end ("Thanks, that's all. Bye!")
  83995 BIT: ' You got it. Take care.'              (no delegation)
  87986 > session.close
  88611 < session.closed reason=close_requested
```
