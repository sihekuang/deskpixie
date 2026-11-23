#!/bin/bash
# End-to-end check of spoken updates (#482, #481), with no one at the mic.
#
# Starts a built DeskPixie on its own data folder, herdr session and hook port, then two stand-in
# sessions of yours (the app's own pix around a stand-in claude, as the UI tests' sessions tier
# does). Each stand-in finishes turns with the replies below, the app announces them in a
# conversation opened only to speak (agent.listenAfterUpdates "never": no mic), and this script
# reads the app's voice log: was each update said, how long it took, how its audio arrived.
#
#   scripts/e2e-voice-updates.sh [path/to/DeskPixie.app] [openai-live|gemini-live|openai-realtime]
#
# Uses the voice service's API key from the Keychain, as the app does: a few cents a run. Pixie
# speaks through this Mac's speaker. The user's own DeskPixie, settings, coordinator and pix are
# not touched: everything is under /tmp/dpv-<tag>, removed at the end (KEEP=1 keeps it).
set -euo pipefail
# Run from inside a pix session (a terminal or agent wrapped by pix), PIX_* would make each
# stand-in's pix step aside and send its hooks to that outer session, which reports to the
# user's own DeskPixie. Nothing of the caller's pix reaches this run.
for v in $(env | sed -n 's/^\(PIX_[A-Za-z0-9_]*\)=.*/\1/p'); do unset "$v"; done

APP=${1:-mac/build/Build/Products/Debug/DeskPixie.app}
PROVIDER=${2:-openai-live}
UPDATE_TIMEOUT=${UPDATE_TIMEOUT:-45}
APP=$(cd "$(dirname "$APP")" && pwd)/$(basename "$APP")
BIN="$APP/Contents/MacOS/DeskPixie"
PIX="$APP/Contents/Helpers/pix"
[ -x "$BIN" ] && [ -x "$PIX" ] || { echo "no built app at $APP (make -C mac build)" >&2; exit 2; }
VERSION=$(defaults read "$APP/Contents/Info.plist" CFBundleShortVersionString)

TAG=$(openssl rand -hex 4)
ROOT=/tmp/dpv-$TAG # short: the pix socket lives under the data folder
SUPPORT=$ROOT/support
SESSION=dp-e2e-$TAG
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
mkdir -p "$SUPPORT" "$ROOT/home" "$ROOT/projects" "$ROOT/bin"

# The updates, as each stand-in session's turn ends with them.
UPDATES=(
  "alpha|I fixed the flaky upload test; all 212 tests pass now."
  "beta|The staging deploy finished and the health check is green."
  "alpha|Should I open a pull request for the upload fix, or keep going on the retry logic?"
)

APP_PID=""
SESSION_PIDS=()
cleanup() {
  set +eu
  for p in ${SESSION_PIDS[@]+"${SESSION_PIDS[@]}"}; do kill "$p" 2>/dev/null || true; done
  [ -n "$APP_PID" ] && kill "$APP_PID" 2>/dev/null || true
  pkill -f "$ROOT/" 2>/dev/null || true # anything left that runs from this run's folder (cat)
  sleep 1
  if command -v herdr >/dev/null; then
    herdr session stop "$SESSION" >/dev/null 2>&1 || true
    herdr session delete "$SESSION" >/dev/null 2>&1 || true
  fi
  if [ "${KEEP:-}" = 1 ]; then echo "kept $ROOT"; else rm -rf "$ROOT"; fi
}
trap cleanup EXIT

python3 - "$SUPPORT/settings.json" "$SESSION" "$PORT" "$ROOT/projects" "$PROVIDER" <<'EOF'
import json, sys
path, session, port, projects, provider = sys.argv[1:]
json.dump({
    "hotkey": "ctrl+alt+shift+cmd+F19",  # one the user's DeskPixie doesn't hold
    "canvas": {"autoOpen": False, "keep": 50},
    "agent": {"enabled": True, "warm": False, "session": session, "port": int(port),
              "keepPanesOnQuit": False, "projectsRoot": projects,
              "openConversationForUpdates": True, "listenAfterUpdates": "never"},
    "notifications": {"sessionTurns": True, "sessionTurnsMinSeconds": 0},
    "voice": {"provider": provider, "noiseFilter": False},
}, open(path, "w"), indent=2)
EOF

# A stand-in claude: each JSON file put in $TURNS goes to `pix hook <event>`, the event named by
# the file's name up to its first "-", as claude's hooks would send it.
cat > "$ROOT/bin/claude" <<EOF
#!/bin/sh
while kill -0 \$PPID 2>/dev/null; do
  for f in "\$TURNS"/*.json; do
    [ -e "\$f" ] || continue
    ev=\$(basename "\$f")
    "$PIX" hook "\${ev%%-*}" < "\$f" >> "\$TURNS.log" 2>&1
    rm -f "\$f"
  done
  sleep 0.2
done
EOF
chmod +x "$ROOT/bin/claude"

echo "app: $APP ($VERSION), voice: $PROVIDER, data: $ROOT"
DESKPIXIE_SUPPORT_DIR=$SUPPORT "$BIN" -DeskPixieOnboardingDone YES \
  -DeskPixiePermissionsCheckedVersion "$VERSION" -ApplePersistenceIgnoreState YES \
  < /dev/null > "$ROOT/app.log" 2>&1 &
APP_PID=$!
for _ in $(seq 1 100); do [ -S "$SUPPORT/agent/pix.sock" ] && break; sleep 0.2; done
[ -S "$SUPPORT/agent/pix.sock" ] || { echo "the app never opened its pix socket" >&2; tail -20 "$ROOT/app.log" >&2; exit 1; }

start_session() { # name
  local turns=$ROOT/turns-$1 fifo=$ROOT/input-$1
  mkdir -p "$turns"
  mkfifo "$fifo"
  (exec sleep 86400) > "$fifo" 2>/dev/null & # holds it open: the session's input never ends
  SESSION_PIDS+=($!)
  # script refuses a FIFO as its input: it reads a pipe from cat, which reads the FIFO.
  cd "$ROOT/projects"
  cat "$fifo" | HOME=$ROOT/home DESKPIXIE_SUPPORT_DIR=$SUPPORT TERM=xterm-256color \
     PATH="$ROOT/bin:/usr/bin:/bin" TURNS=$turns \
     /usr/bin/script -q /dev/null "$PIX" --description "$1" claude > /dev/null 2>&1 &
  SESSION_PIDS+=($!) # script: ending it ends its pix and the stand-in
  cd - > /dev/null
}
hook() { # name event json
  local turns=$ROOT/turns-$1
  printf '%s' "$3" > "$turns/.turn"
  mv "$turns/.turn" "$turns/$2-$(openssl rand -hex 6).json"
}

start_session alpha
start_session beta
sleep 3 # pix registers with the app

VOICE=$SUPPORT/logs/voice.log
touch "$VOICE"
results=()
for u in "${UPDATES[@]}"; do
  name=${u%%|*} reply=${u#*|}
  from=$(wc -l < "$VOICE")
  hook "$name" UserPromptSubmit "$(python3 -c 'import json,sys; print(json.dumps({"hook_event_name":"UserPromptSubmit","prompt":"keep going"}))')"
  sleep 2
  hook "$name" Stop "$(python3 -c 'import json,sys; print(json.dumps({"hook_event_name":"Stop","last_assistant_message":sys.argv[1]}))' "$reply")"
  echo "-> $name: $reply"
  done=""
  for _ in $(seq 1 $((UPDATE_TIMEOUT * 5))); do
    if tail -n +$((from + 1)) "$VOICE" | grep -q "conversation closed"; then done=1; break; fi
    sleep 0.2
  done
  results+=("$from|${done:-timeout}")
  sleep 2
done

python3 - "$VOICE" "${results[@]}" <<'EOF'
import re, sys
from datetime import datetime
lines = open(sys.argv[1], encoding="utf-8", errors="replace").read().splitlines()
def ts(l): return datetime.fromisoformat(l.split(" ", 1)[0])
failed = 0
print()
for i, r in enumerate(sys.argv[2:], 1):
    start, state = r.split("|")
    seg = lines[int(start):]
    end = next((k for k, l in enumerate(seg) if "conversation closed" in l), len(seg) - 1)
    seg = seg[:end + 1]
    notice = next((l for l in seg if "notice to Pixie" in l), None)
    spoke = [l.split("pixie: ", 1)[1] for l in seg if "pixie: " in l]
    speaking = next((l for l in seg if "-> Speaking" in l), None)
    flow = [l.split("reply audio: ", 1)[1] for l in seg if "reply audio: " in l]
    warn = [l.split(" ", 1)[1] for l in seg if "warning" in l or "error" in l]
    ok = notice is not None and bool(spoke)
    failed += not ok
    wait = f"{(ts(speaking) - ts(notice)).total_seconds():.1f} s" if notice and speaking else "-"
    print(f"update {i}: {'SAID' if ok else 'NOT SAID'} ({state}); notice -> speaking {wait}")
    for s in spoke: print(f"    pixie: {s}")
    for f in flow: print(f"    audio: {f}")
    for w in warn: print(f"    {w}")
    if seg: print(f"    closed: {seg[-1].split('conversation closed: ', 1)[-1]}")
print()
print("PASS" if not failed else f"FAIL: {failed} update(s) not said")
sys.exit(1 if failed else 0)
EOF
