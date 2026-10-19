package agent

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSessionGist(t *testing.T) {
	long := strings.Repeat("everything ", 20) + "works."
	for _, tc := range []struct {
		name, reply, want string
		asking            bool
	}{
		{"plain done", "The redirect loop is fixed and all 412 tests pass.", "The redirect loop is fixed and all 412 tests pass.", false},
		{"outcome last", "I traced the loop to the cookie path.\n\nThe redirect loop is fixed and all 412 tests pass.", "The redirect loop is fixed and all 412 tests pass.", false},
		{"question last", "I looked at it.\n\nShould I also update the docs?", "Should I also update the docs?", true},
		{"question after a sentence", "Fixed the loop. Want me to update the docs too?", "Want me to update the docs too?", true},
		{"short then paragraph before", "Changed the cookie path to slash.\n\nDone.", "Changed the cookie path to slash.", false},
		{"short then next sentence", "Done. All 412 tests pass.", "All 412 tests pass.", false},
		{"code only", "```go\nfunc main() {}\n```", "", false},
		{"list only", "## Summary\n\n- **Fixed** the `login` redirect\n- Added a test", "Fixed the login redirect.", false},
		{"table then words", "| a | b |\n|---|---|\n| 1 | 2 |\n\nAll green now.", "All green now.", false},
		{"snake_case kept", "Renamed load_config to read_config everywhere.", "Renamed load_config to read_config everywhere.", false},
		{"empty", "  \n\n ", "", false},
		{"question as a list", "I've fixed it.\n\nWould you like me to:\n- commit\n- push", "Would you like me to commit or push?", true},
		{"question as a list of three", "Which should I do next?\n1. tests\n2. docs\n3. release", "Which should I do next: tests, docs or release?", true},
		{"question, blank line, list", "Done with the fix.\n\nShould I:\n\n- **commit** it\n- open a PR", "Should I commit it or open a PR?", true},
		{"list that is not a question", "Changes:\n- fixed the loop\n- added a test", "Changes: fixed the loop.", false},
		// Voice log, Oct 18: these went to Pixie as "is done: how do you want to proceed?" and
		// "is asking: which do you want?" with the options gone.
		{"question then one more sentence", "Both builds failed.\n\nHow do you want to proceed? I can rerun them or skip CI.", "How do you want to proceed? I can rerun them or skip CI.", true},
		{"should I, then a note", "The suite passed. Should I run both? It takes ten minutes.", "Should I run both? It takes ten minutes.", true},
		{"a question that isn't for you", "Why did it fail? The cache was stale, so I cleared it.", "Why did it fail?", false},
		{"short question after a list", "Two ways:\n\n1. **Restart the app.** I'd test it myself.\n2. **No restart, you speak two requests.** I'd watch the logs.\n\nEither way, I'd make a scratch repo.\n\nWhich do you want?", "Which do you want: restart the app or no restart, you speak two requests?", true},
	} {
		got, asking := SessionGist(tc.reply)
		if got != tc.want || asking != tc.asking {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got, asking, tc.want, tc.asking)
		}
	}
	got, _ := SessionGist(long)
	if utf8.RuneCountInString(got) > MaxGistRunes || !strings.HasSuffix(got, "…") || strings.Contains(got, "everythi…") {
		t.Errorf("capped on a word boundary: %q", got)
	}
}

func TestSessionTurnLine(t *testing.T) {
	for _, tc := range []struct {
		label, gist string
		asking      bool
		want        string
	}{
		{"deskpixie: fix login redirect", "The redirect loop is fixed and all 412 tests pass.", false,
			"deskpixie: fix login redirect is done: the redirect loop is fixed and all 412 tests pass."},
		{"api", "Should I also update the docs?", true, "api is asking: should I also update the docs?"},
		{"api", "", false, "api finished."},
		{"api", "API keys rotated", false, "api is done: API keys rotated."},
		{"api", "I traced it to the cookie path.", false, "api is done: I traced it to the cookie path."},
		{"api", "Cut at a word…", false, "api is done: cut at a word…"},
	} {
		if got := SessionTurnLine(tc.label, tc.gist, tc.asking); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
