package agent_test

import (
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	. "github.com/sihekuang/deskpixie/core/agent"
)

// #170: "show me the api session": Pixie's focus_session brings one of your sessions to the
// front, as a click on its roster row does.
func TestFocusSessionShowsTheSessionYouAskedFor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, false)
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		got := invoke(t, FocusSessionTool{H: r.h}, `{"name":"api-claude"}`)
		if want := map[string]any{"name": "api-claude", "status": "shown"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v; want %v", got, want)
		}
		if sent := r.focus.Sent(); len(sent) != 1 || !reflect.DeepEqual(sent[0], FocusPlan{FocusURLs: []string{"warp://session/abc"}}) {
			t.Fatalf("its plan goes to the app: %+v", sent)
		}
	})
}

// A name that isn't one of your sessions (closed since, misheard, or Pixie's own coordinator)
// shows nothing; the reply lists your sessions as they are now, so Pixie can ask which.
func TestFocusSessionListsYourSessionsWhenTheNameIsNotOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		r.user.connect(t, "api-claude")
		r.user.connect(t, "web-claude")
		time.Sleep(time.Second)
		for _, args := range []string{`{"name":"deskpixie-claude"}`, `{"name":"` + Coordinator + `"}`, `{}`} {
			got, _ := invoke(t, FocusSessionTool{H: r.h}, args).(map[string]any)
			if msg, _ := got["error"].(string); msg == "" {
				t.Fatalf("%s: no error: %v", args, got)
			}
			sessions, _ := got["your_sessions"].([]any)
			if len(sessions) != 2 || !strings.HasPrefix(sessions[0].(string), "api-claude") || !strings.HasPrefix(sessions[1].(string), "web-claude") {
				t.Fatalf("%s: %v", args, got)
			}
		}
		if len(r.focus.Sent()) != 0 {
			t.Fatalf("nothing shown: %v", r.focus.Sent())
		}
	})
}

// Pixie's memory names your sessions, so "the api one" can be matched; Pixie's own panes aren't
// listed, and with no session of yours there's no such part.
func TestPixiesMemoryListsYourSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRosterRig(t, true)
		time.Sleep(time.Second)
		if facts := r.h.Facts(); strings.Contains(facts, "sessions") {
			t.Fatalf("no sessions yet: %q", facts)
		}
		r.user.connect(t, "api-claude")
		time.Sleep(time.Second)
		facts := r.h.Facts()
		if !strings.Contains(facts, "focus_session") || !strings.Contains(facts, "\n- api-claude: claude in api (iTerm)") {
			t.Fatalf("%q", facts)
		}
		if strings.Contains(facts, Coordinator) {
			t.Fatalf("Pixie's own panes are listed: %q", facts)
		}
	})
}
