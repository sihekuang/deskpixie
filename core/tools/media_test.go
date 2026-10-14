package tools

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeShell answers by the command it is given: "pgrep X" fails unless X is running;
// osascript answers from answers by the script's text (default: empty success).
type fakeShell struct {
	running []string
	answers map[string]shellAnswer
	mu      sync.Mutex
	ran     []string
}

type shellAnswer struct {
	out string
	err string
}

func shellWith(running []string, answers map[string]shellAnswer) *fakeShell {
	return &fakeShell{running: running, answers: answers}
}

func (f *fakeShell) scripts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.ran {
		if !strings.HasPrefix(c, "pgrep") {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeShell) Run(_ context.Context, program string, args ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(program, "pgrep") {
		app := args[len(args)-1]
		f.ran = append(f.ran, "pgrep "+app)
		for _, r := range f.running {
			if r == app {
				return "123", nil
			}
		}
		return "", errors.New("")
	}
	script := args[1]
	f.ran = append(f.ran, script)
	if a, ok := f.answers[script]; ok {
		if a.err != "" {
			return "", errors.New(a.err)
		}
		return a.out, nil
	}
	return "", nil
}

func tell(app, cmd string) string { return "tell application \"" + app + "\"\n" + cmd + "\nend tell" }

func ok(s string) shellAnswer  { return shellAnswer{out: s} }
func bad(s string) shellAnswer { return shellAnswer{err: s} }

func act(sh *fakeShell, a MediaAction) (string, string, []string) {
	p := &MacPlayers{Shell: sh, Sleep: func(time.Duration) {}}
	out, err := p.Act(context.Background(), a)
	e := ""
	if err != nil {
		e = err.Error()
	}
	return out, e, sh.scripts()
}

func TestPauseGoesToThePlayerThatIsPlaying(t *testing.T) {
	sh := shellWith([]string{"Spotify", "Music"}, map[string]shellAnswer{
		tell("Spotify", "player state as string"): ok("paused"),
		tell("Music", "player state as string"):   ok("playing"),
	})
	out, e, scripts := act(sh, MediaAction{Kind: Pause})
	if out != "Paused Music." || e != "" || scripts[len(scripts)-1] != tell("Music", "pause") {
		t.Fatal(out, e, scripts)
	}
}

func TestWithNothingPlayingTheFirstOpenPlayerIsUsed(t *testing.T) {
	sh := shellWith([]string{"Music"}, map[string]shellAnswer{tell("Music", "player state as string"): ok("paused")})
	if out, e, _ := act(sh, MediaAction{Kind: Play}); out != "Playing in Music." || e != "" {
		t.Fatal(out, e)
	}
}

func TestAPlayerThatIsNotRunningIsNeverNamed(t *testing.T) {
	_, _, scripts := act(shellWith([]string{"Music"}, nil), MediaAction{Kind: Pause})
	for _, s := range scripts {
		if strings.Contains(s, "Spotify") {
			t.Fatal(scripts)
		}
	}
}

func TestNextSaysTheNewTrack(t *testing.T) {
	sh := shellWith([]string{"Spotify"}, map[string]shellAnswer{
		tell("Spotify", "player state as string"): ok("playing"),
		tell("Spotify", trackScript):              ok("Song 2 by Blur"),
	})
	out, _, scripts := act(sh, MediaAction{Kind: Next})
	if out != "Skipped. Now: Song 2 by Blur, in Spotify." {
		t.Fatal(out)
	}
	found := false
	for _, s := range scripts {
		found = found || s == tell("Spotify", "next track")
	}
	if !found {
		t.Fatal(scripts)
	}
}

func TestASkipThatWorkedIsReportedEvenIfTheNewTrackCantBeRead(t *testing.T) {
	sh := shellWith([]string{"Music"}, map[string]shellAnswer{tell("Music", trackScript): bad("execution error: -1728")})
	if out, e, _ := act(sh, MediaAction{Kind: Previous}); out != "Back one." || e != "" {
		t.Fatal(out, e)
	}
}

func TestARefusedPermissionSaysWhereToAllowIt(t *testing.T) {
	sh := shellWith([]string{"Music"}, map[string]shellAnswer{
		tell("Music", "pause"): bad("49:57: execution error: Not authorized to send Apple events to Music. (-1743)"),
	})
	_, e, _ := act(sh, MediaAction{Kind: Pause})
	// Rust said "AI Mascot"; renamed per the parity plan's rename rule.
	if e != "DeskPixie isn't allowed to control Music: turn it on in System Settings, Privacy & Security, Automation" {
		t.Fatal(e)
	}
}

func TestAPlayerWaitingOnThePermissionPromptStopsTheSearch(t *testing.T) {
	sh := shellWith([]string{"Spotify", "Music"}, map[string]shellAnswer{tell("Spotify", "player state as string"): bad("timeout")})
	_, e, scripts := act(sh, MediaAction{Kind: Pause})
	if e != "Spotify didn't answer; macOS may be asking for permission to control it" {
		t.Fatal(e)
	}
	if len(scripts) != 1 || scripts[0] != tell("Spotify", "player state as string") { // Music isn't asked, nothing is paused
		t.Fatal(scripts)
	}
}

func TestARefusedPlayerIsSkippedForOneThatWorks(t *testing.T) {
	refused := bad("execution error: Not authorized to send Apple events to Spotify. (-1743)")
	sh := shellWith([]string{"Spotify", "Music"}, map[string]shellAnswer{tell("Spotify", "player state as string"): refused})
	out, _, scripts := act(sh, MediaAction{Kind: Pause})
	if out != "Paused Music." || scripts[len(scripts)-1] != tell("Music", "pause") {
		t.Fatal(out, scripts)
	}
	sh = shellWith([]string{"Spotify"}, map[string]shellAnswer{tell("Spotify", "player state as string"): refused})
	if _, e, _ := act(sh, MediaAction{Kind: Pause}); !strings.HasPrefix(e, "DeskPixie isn't allowed to control Spotify") {
		t.Fatal(e)
	}
}

func TestStartingMusicColdIsNotAFailure(t *testing.T) {
	sh := shellWith(nil, map[string]shellAnswer{tell("Music", "play"): bad("timeout")})
	if out, e, _ := act(sh, MediaAction{Kind: Play}); out != "Music is starting." || e != "" {
		t.Fatal(out, e)
	}
}

func TestNowPlayingWithThePlayerStoppedSaysSo(t *testing.T) {
	sh := shellWith([]string{"Music"}, map[string]shellAnswer{tell("Music", trackScript): ok("")})
	if out, _, _ := act(sh, MediaAction{Kind: NowPlaying}); out != "Nothing is playing in Music." {
		t.Fatal(out)
	}
}

func TestWithNoPlayerOpenPlayStartsMusicAndTheRestFail(t *testing.T) {
	out, e, scripts := act(shellWith(nil, nil), MediaAction{Kind: Play})
	if out != "Playing in Music." || e != "" || len(scripts) != 1 || scripts[0] != tell("Music", "play") {
		t.Fatal(out, e, scripts)
	}
	out, e, scripts = act(shellWith(nil, nil), MediaAction{Kind: Next})
	if out != "" || e != "no music app is open" || len(scripts) != 0 {
		t.Fatal(out, e, scripts)
	}
}

func TestVolumeNeedsNoPlayer(t *testing.T) {
	out, _, scripts := act(shellWith(nil, nil), MediaAction{Kind: Volume, Level: 30})
	if out != "Volume set to 30%." || len(scripts) != 1 || scripts[0] != "set volume output volume 30" {
		t.Fatal(out, scripts)
	}
}

func TestASlowPlayerIsExplained(t *testing.T) {
	sh := shellWith([]string{"Music"}, map[string]shellAnswer{tell("Music", "pause"): bad("timeout")})
	if _, e, _ := act(sh, MediaAction{Kind: Pause}); e != "Music didn't answer; macOS may be asking for permission to control it" {
		t.Fatal(e)
	}
}

type canned struct {
	out string
	err string
}

func (c canned) Act(context.Context, MediaAction) (string, error) {
	if c.err != "" {
		return "", errors.New(c.err)
	}
	return c.out, nil
}

func TestTheToolChecksItsArguments(t *testing.T) {
	tl := Media{Player: canned{out: "Paused Music."}}
	eq(t, call(t, tl, `{"action":"pause"}`), `{"done":"Paused Music."}`)
	eq(t, call(t, tl, `{"action":"volume","level":50.0}`), `{"done":"Paused Music."}`) // a float level is fine
	eq(t, call(t, tl, `{"action":"volume"}`), `{"error":"volume needs a level from 0 to 100"}`)
	eq(t, call(t, tl, `{"action":"volume","level":101}`), `{"error":"volume needs a level from 0 to 100"}`)
	eq(t, call(t, tl, `{"action":"shuffle"}`), `{"error":"action must be play, pause, next, previous, volume or now_playing"}`)
	failing := Media{Player: canned{err: "no music app is open"}}
	eq(t, call(t, failing, `{"action":"next"}`), `{"error":"no music app is open"}`)
}

// Extra (no Rust twin): the real shell runner, exercised with harmless commands.
func TestSystemShell(t *testing.T) {
	out, err := SystemShell{}.Run(context.Background(), "/bin/echo", "hi")
	if err != nil || out != "hi" {
		t.Fatal(out, err)
	}
	if _, err := (SystemShell{}).Run(context.Background(), "/bin/sh", "-c", "echo oops >&2; exit 3"); err == nil || err.Error() != "oops" {
		t.Fatal(err)
	}
}
