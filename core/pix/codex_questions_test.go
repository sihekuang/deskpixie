package pix

import (
	"strings"
	"testing"
	"time"
)

// #268: a Codex question (request_user_input) is held in its PreToolUse hook as Claude's are.
// Codex's hook can't hand back answers in the tool's input, so the answer is its refusal's
// reason, which Codex reads as the tool's result.

var codexAsk = map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
	map[string]any{"id": "database", "header": "Database", "question": "Which database?",
		"options": []any{map[string]any{"label": "Postgres", "description": "Relational"}, map[string]any{"label": "SQLite"}}}}}}

func codexHoldHarness(t *testing.T, wait time.Duration) *harness {
	t.Helper()
	h := keyHarness(t, "codex")
	h.s.setHold(wait)
	return h
}

func TestAHeldCodexQuestionIsAnsweredThroughItsHook(t *testing.T) {
	for _, c := range []struct{ choice, want string }{
		{"2", `"Which database?" -> "SQLite"`},
		{"text:  MariaDB  ", `"Which database?" -> "MariaDB" (their own answer, not one of the options)`},
		{"chat", `talk this over before answering "Which database?"`},
	} {
		t.Run(c.choice, func(t *testing.T) {
			h := codexHoldHarness(t, 5*time.Second)
			reply := hookAsync(h, "PreToolUse", codexAsk)
			q := askedQuestion(t, h)
			if q.Source != "menu" || strings.Join(q.Keys, "|") != "Postgres|SQLite" || !q.Typed ||
				strings.Join(q.Details, "|") != "Relational|" {
				t.Fatalf("%+v", q)
			}
			if err := answer(t, h, q.ID, c.choice); err != "" {
				t.Fatal(err)
			}
			out := hookReply(t, reply, time.Second)
			specific, _ := out["hookSpecificOutput"].(map[string]any)
			reason, _ := specific["permissionDecisionReason"].(string)
			if specific["hookEventName"] != "PreToolUse" || specific["permissionDecision"] != "deny" ||
				!strings.Contains(reason, c.want) || !strings.HasPrefix(reason, "The user answered in Pixie") && c.choice != "chat" {
				t.Fatalf("%v", out)
			}
		})
	}
}

// Nobody answers in time: the hook lets go and Codex shows its question in the terminal.
func TestAHeldCodexQuestionNobodyAnswersGoesToTheTerminal(t *testing.T) {
	h := codexHoldHarness(t, 200*time.Millisecond)
	reply := hookAsync(h, "PreToolUse", codexAsk)
	askedQuestion(t, h)
	if out := hookReply(t, reply, 2*time.Second); out != nil {
		t.Fatalf("%v", out)
	}
}

// Without a hold (or one Pixie can't show: a secret, several questions) the question is only
// announced: pix answers no Codex question by keys.
func TestACodexQuestionPixieCantHoldIsOnlyAnnounced(t *testing.T) {
	secret := map[string]any{"tool_name": "request_user_input", "tool_input": map[string]any{"questions": []any{
		map[string]any{"id": "token", "question": "Your token?", "isSecret": true,
			"options": []any{map[string]any{"label": "Skip"}}}}}}
	for name, c := range map[string]struct {
		wait time.Duration
		p    map[string]any
	}{"no hold": {0, codexAsk}, "secret": {5 * time.Second, secret}} {
		t.Run(name, func(t *testing.T) {
			h := codexHoldHarness(t, c.wait)
			reply := hookAsync(h, "PreToolUse", c.p)
			if out := hookReply(t, reply, time.Second); out != nil {
				t.Fatalf("%v", out)
			}
			if q := askedQuestion(t, h); len(q.Keys) != 0 || q.Typed {
				t.Fatalf("%+v", q)
			}
		})
	}
}

// Claude's own menu tool name means nothing to Codex, and Codex's nothing to Claude.
func TestEachAgentHoldsOnlyItsOwnQuestionTool(t *testing.T) {
	claudeInCodex := map[string]any{"tool_name": "AskUserQuestion", "tool_input": codexAsk["tool_input"]}
	h := codexHoldHarness(t, 5*time.Second)
	if out := hookReply(t, hookAsync(h, "PreToolUse", claudeInCodex), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
	h = holdHarness(t, 5*time.Second)
	if out := hookReply(t, hookAsync(h, "PreToolUse", codexAsk), time.Second); out != nil {
		t.Fatalf("%v", out)
	}
}
