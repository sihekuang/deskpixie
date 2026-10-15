package pix

import "testing"

// With `alias claude='pix claude'`, scripted and one-shot runs must behave exactly like plain
// claude: no PTY (CRLF, stderr merged, piped stdin as keys) and no Pixie session.
func TestPassthrough(t *testing.T) {
	for _, c := range []struct {
		tool     string
		args     []string
		in, out  bool
		want     bool
		describe string
	}{
		{"claude", nil, true, true, false, "interactive claude is wrapped"},
		{"claude", []string{"--resume", "x"}, true, true, false, "resume is interactive"},
		{"claude", []string{"-p", "hi"}, true, true, true, "print mode"},
		{"claude", []string{"--model", "opus", "--print", "hi"}, true, true, true, "print mode after other flags"},
		{"claude", []string{"mcp", "list"}, true, true, true, "a subcommand"},
		{"claude", []string{"--version"}, true, true, true, "version"},
		{"claude", nil, false, true, true, "stdin piped"},
		{"claude", nil, true, false, true, "stdout piped"},
		{"codex", nil, true, true, false, "interactive codex is wrapped"},
		{"codex", []string{"resume", "--last"}, true, true, false, "codex resume is interactive"},
		{"codex", []string{"exec", "fix it"}, true, true, true, "codex exec"},
		{"codex", []string{"-m", "o3", "e", "fix it"}, true, true, true, "codex e after a flag"},
		{"codex", []string{"login"}, true, true, true, "codex login"},
		{"codex", []string{"fix the tests"}, true, true, false, "a prompt is not a subcommand"},
	} {
		if got := Passthrough(c.tool, c.args, c.in, c.out); got != c.want {
			t.Errorf("%s: Passthrough(%s %v, stdin tty %v, stdout tty %v) = %v", c.describe, c.tool, c.args, c.in, c.out, got)
		}
	}
}
