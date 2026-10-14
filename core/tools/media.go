package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MediaKind is what to do to the music player.
type MediaKind int

const (
	Play MediaKind = iota
	Pause
	Next
	Previous
	// Volume sets the computer's output volume to MediaAction.Level (0 to 100).
	Volume
	NowPlaying
)

type MediaAction struct {
	Kind  MediaKind
	Level int
}

// MediaPlayer is the music player boundary. It returns a short sentence the model can say, or why it failed.
type MediaPlayer interface {
	Act(ctx context.Context, a MediaAction) (string, error)
}

// Media is `media`: play, pause, skip and volume for the music app that's playing (macOS: Music
// or Spotify, via AppleScript). Playing something specific ("play some jazz") still goes to delegate.
type Media struct{ Player MediaPlayer }

func (Media) Name() string { return "media" }

func (Media) Description() string {
	return "Control the music that's playing on this Mac (Apple Music or Spotify): play, pause, next, " +
		"previous, set the volume, or say what's playing. To play a particular song, artist or playlist, " +
		"use delegate instead."
}

func (Media) Parameters() json.RawMessage {
	return json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": ["play", "pause", "next", "previous", "volume", "now_playing"] },
    "level": { "type": "integer", "minimum": 0, "maximum": 100, "description": "For volume: 0 to 100." }
  },
  "required": ["action"],
  "additionalProperties": false
}`)
}

func (m Media) Invoke(ctx context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
	a := parseArgs(raw)
	action, _ := a.str("action")
	var act MediaAction
	switch action {
	case "play":
		act.Kind = Play
	case "pause":
		act.Kind = Pause
	case "next":
		act.Kind = Next
	case "previous":
		act.Kind = Previous
	case "now_playing":
		act.Kind = NowPlaying
	case "volume":
		l, ok := a.float("level")
		if !ok || l < 0 || l > 100 {
			return fail("volume needs a level from 0 to 100")
		}
		act = MediaAction{Kind: Volume, Level: int(math.Round(l))}
	default:
		return fail("action must be play, pause, next, previous, volume or now_playing")
	}
	said, err := m.Player.Act(ctx, act)
	if err != nil {
		return fail(err.Error())
	}
	return reply(map[string]any{"done": said})
}

// Shell runs a program and returns its trimmed stdout; an error on failure, a non-zero exit or a timeout.
type Shell interface {
	Run(ctx context.Context, program string, args ...string) (string, error)
}

// PlayerTimeout is the longest a player command may take. The first command to an app can wait
// on macOS's "control Music?" permission prompt; it then times out and works once the user answers.
const PlayerTimeout = 5 * time.Second

type SystemShell struct{}

func (SystemShell) Run(ctx context.Context, program string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, PlayerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", errors.New("timeout")
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", errors.New(strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// MacPlayers drives Apple Music and Spotify with AppleScript. An app is only named in a script
// once pgrep shows it running, so a Mac without Spotify never gets a "Where is Spotify?" dialog.
type MacPlayers struct {
	Shell Shell
	// Sleep waits for a player to show the new track after a skip; nil means time.Sleep.
	Sleep func(time.Duration)
}

// NewMacPlayers is the real thing.
func NewMacPlayers() *MacPlayers { return &MacPlayers{Shell: SystemShell{}} }

// players are process names as AppleScript and pgrep know them. Spotify first: if both run, it
// is usually the one the user means.
var players = []string{"Spotify", "Music"}

// skipSettle is how long a player takes to show the new track after a skip.
const skipSettle = 300 * time.Millisecond

// trackScript yields "Name by Artist", or "" when nothing is loaded (Spotify reports "paused" with no track).
const trackScript = "if player state is stopped then return \"\"\ntry\nset t to name of current track\non error\nreturn \"\"\nend try\nif t is \"\" then return \"\"\nreturn t & \" by \" & (artist of current track)"

func (m *MacPlayers) running(ctx context.Context) []string {
	// Only this user's processes: with fast user switching, another user's Music doesn't count.
	user := os.Getenv("USER")
	var out []string
	for _, app := range players {
		args := []string{"-x", app}
		if user != "" {
			args = []string{"-x", "-U", user, app}
		}
		if _, err := m.Shell.Run(ctx, "/usr/bin/pgrep", args...); err == nil {
			out = append(out, app)
		}
	}
	return out
}

func (m *MacPlayers) tell(ctx context.Context, app, command string) (string, error) {
	script := fmt.Sprintf("tell application \"%s\"\n%s\nend tell", app, command)
	out, err := m.Shell.Run(ctx, "/usr/bin/osascript", "-e", script)
	if err != nil {
		e := err.Error()
		switch {
		case e == "timeout":
			return "", fmt.Errorf("%s didn't answer; macOS may be asking for permission to control it", app)
		case strings.Contains(e, "-1743"):
			// Rust said "AI Mascot"; renamed per the parity plan's rename rule.
			return "", fmt.Errorf("DeskPixie isn't allowed to control %s: turn it on in System Settings, Privacy & Security, Automation", app)
		}
		return "", fmt.Errorf("%s: %s", app, e)
	}
	return out, nil
}

// current is the running player that is playing, else the first running one we may control. A
// player that doesn't answer (macOS asking for permission) stops the search, so the user hears
// why at once; one the user refused is skipped, and its error is returned only if no other
// player is open. "" means no player is open.
func (m *MacPlayers) current(ctx context.Context) (string, error) {
	var usable []string
	var refused error
	for _, app := range m.running(ctx) {
		state, err := m.tell(ctx, app, "player state as string")
		switch {
		case err == nil && state == "playing":
			return app, nil
		case err != nil && strings.Contains(err.Error(), "didn't answer"):
			return "", err
		case err != nil && strings.Contains(err.Error(), "isn't allowed"):
			refused = err
		default:
			usable = append(usable, app)
		}
	}
	if len(usable) > 0 {
		return usable[0], nil
	}
	return "", refused
}

func (m *MacPlayers) track(ctx context.Context, app string) (string, error) {
	said, err := m.tell(ctx, app, trackScript)
	if err != nil {
		return "", err
	}
	if said == "" {
		return fmt.Sprintf("Nothing is playing in %s.", app), nil
	}
	return fmt.Sprintf("%s, in %s.", said, app), nil
}

// afterSkip reports the new track if it can be read (players update a moment later), else just
// that the skip worked, so the model never retries a skip that happened.
func (m *MacPlayers) afterSkip(ctx context.Context, app, done string) string {
	if m.Sleep != nil {
		m.Sleep(skipSettle)
	} else {
		time.Sleep(skipSettle)
	}
	if t, err := m.track(ctx, app); err == nil {
		return done + " Now: " + t
	}
	return done
}

func (m *MacPlayers) Act(ctx context.Context, a MediaAction) (string, error) {
	if a.Kind == Volume {
		if _, err := m.Shell.Run(ctx, "/usr/bin/osascript", "-e", fmt.Sprintf("set volume output volume %d", a.Level)); err != nil {
			return "", err
		}
		return fmt.Sprintf("Volume set to %d%%.", a.Level), nil
	}
	app, err := m.current(ctx)
	if err != nil {
		return "", err
	}
	if app == "" {
		if a.Kind != Play {
			return "", errors.New("no music app is open")
		}
		// Starting Music cold can outlast the timeout; the play command still lands.
		if _, err := m.tell(ctx, "Music", "play"); err != nil {
			if strings.Contains(err.Error(), "didn't answer") {
				return "Music is starting.", nil
			}
			return "", err
		}
		return "Playing in Music.", nil
	}
	switch a.Kind {
	case Play:
		if _, err := m.tell(ctx, app, "play"); err != nil {
			return "", err
		}
		return fmt.Sprintf("Playing in %s.", app), nil
	case Pause:
		if _, err := m.tell(ctx, app, "pause"); err != nil {
			return "", err
		}
		return fmt.Sprintf("Paused %s.", app), nil
	case Next:
		if _, err := m.tell(ctx, app, "next track"); err != nil {
			return "", err
		}
		return m.afterSkip(ctx, app, "Skipped."), nil
	case Previous:
		if _, err := m.tell(ctx, app, "previous track"); err != nil {
			return "", err
		}
		return m.afterSkip(ctx, app, "Back one."), nil
	case NowPlaying:
		return m.track(ctx, app)
	}
	return "", errors.New("unsupported action")
}
