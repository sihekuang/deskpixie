package herdr

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Port of supervisor.rs tests (21). The session in server_env fixtures is the app's default
// session, renamed ai-mascot → deskpixie (spec §6); the captured status fixture keeps its own.

const (
	supBin     = "/Users/x/.local/bin/herdr"
	supSession = "plan-probe-11827" // the fixture's session
)

func notRunning(t *testing.T) string {
	s := fixture(t, "status_running.json")
	s = strings.Replace(s, `"running":true`, `"running":false`, 1)
	return strings.Replace(s, `"compatible":true`, `"compatible":null`, 1)
}

func supervisorFor(t *testing.T, r *fakeRunner) *Supervisor {
	t.Helper()
	s, err := NewSupervisor(supBin, []agent.EnvVar{{Key: "HERDR_SESSION", Value: supSession}}, r)
	if err != nil {
		t.Fatal(err)
	}
	return s.WithTiming(10*time.Millisecond, 200*time.Millisecond)
}

func statusCmd() string { return supBin + " status --json" }

func integrationCurrent(t *testing.T, r *fakeRunner) {
	r.answer(supBin+" integration status", runOK(fixture(t, "integration_status.txt")))
}

func pairs(kv ...string) []agent.EnvVar {
	var out []agent.EnvVar
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, agent.EnvVar{Key: kv[i], Value: kv[i+1]})
	}
	return out
}

func envKeys(env []agent.EnvVar) []string {
	var out []string
	for _, e := range env {
		out = append(out, e.Key)
	}
	return out
}

func envGet(env []agent.EnvVar, k string) string {
	for _, e := range env {
		if e.Key == k {
			return e.Value
		}
	}
	return ""
}

func TestTheEnvIsScrubbedAndNamesTheSession(t *testing.T) {
	parent := pairs(
		"ANTHROPIC_API_KEY", "sk-ant-x",
		"ANTHROPIC_AUTH_TOKEN", "t",
		"ANTHROPIC_BASE_URL", "u",
		"OPENAI_API_KEY", "sk-x",
		"GEMINI_API_KEY", "AIza-x",
		"CLAUDE_CODE_USE_BEDROCK", "1",
		"CLAUDE_CODE_SESSION_ID", "s",
		"CLAUDECODE", "1",
		"HERDR_SOCKET_PATH", "/Users/x/.config/herdr/herdr.sock",
		"HOME", "/Users/x",
		"LANG", "en_US.UTF-8",
	)
	env, err := ServerEnv(parent, "deskpixie", "/Users/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := envKeys(env); !reflect.DeepEqual(got, []string{"HERDR_SESSION", "HOME", "LANG", "PATH"}) {
		t.Fatal(got)
	}
	if envGet(env, "HERDR_SESSION") != "deskpixie" {
		t.Fatal(env)
	}
	if path := envGet(env, "PATH"); !strings.Contains(path, "/Users/x/.local/bin") {
		t.Fatal(path)
	}
}

func TestEveryVoiceProviderKeyIsScrubbed(t *testing.T) {
	// Derived from voice's provider table, so a new provider's key can't slip through.
	vars := voice.KeyVars()
	if !containsStr(vars, "OPENAI_API_KEY") || !containsStr(vars, "GEMINI_API_KEY") {
		t.Fatal(vars)
	}
	var parent []agent.EnvVar
	for _, v := range vars {
		parent = append(parent, agent.EnvVar{Key: v, Value: "secret"})
	}
	parent = append(parent, agent.EnvVar{Key: "HOME", Value: "/h"})
	env, err := ServerEnv(parent, "deskpixie", "/h")
	if err != nil {
		t.Fatal(err)
	}
	if got := envKeys(env); !reflect.DeepEqual(got, []string{"HERDR_SESSION", "HOME", "PATH"}) {
		t.Fatal(got)
	}
}

func only(want string) func(string) bool { return func(p string) bool { return p == want } }

func findOK(t *testing.T, got string, ok bool, want string) {
	t.Helper()
	if want == "" {
		if ok {
			t.Fatalf("got %q, want none", got)
		}
		return
	}
	if !ok || got != want {
		t.Fatalf("got %q (%v), want %q", got, ok, want)
	}
}

func TestHerdrIsFoundByConfigPathOrLocalBin(t *testing.T) {
	home := "/Users/x"
	p, ok := FindHerdr("/opt/h", "", home, only("/opt/h"))
	findOK(t, p, ok, "/opt/h")
	p, ok = FindHerdr("/opt/h", "", home, only("/nope"))
	findOK(t, p, ok, "") // a configured path must exist
	p, ok = FindHerdr("", "/usr/bin:/opt/homebrew/bin", home, only("/opt/homebrew/bin/herdr"))
	findOK(t, p, ok, "/opt/homebrew/bin/herdr")
	p, ok = FindHerdr("", "/usr/bin", home, only("/Users/x/.local/bin/herdr"))
	findOK(t, p, ok, "/Users/x/.local/bin/herdr")
	p, ok = FindHerdr("", "/usr/bin", home, func(string) bool { return false })
	findOK(t, p, ok, "")
}

func TestHerdrIsFoundInTheUsualInstallDirsToo(t *testing.T) {
	home := "/Users/x"
	// A GUI app's PATH is minimal: the dirs ServerEnv adds are searched as well.
	p, ok := FindHerdr("", "/usr/bin", home, only("/opt/homebrew/bin/herdr"))
	findOK(t, p, ok, "/opt/homebrew/bin/herdr")
	p, ok = FindHerdr("", "", home, only("/usr/local/bin/herdr"))
	findOK(t, p, ok, "/usr/local/bin/herdr")
	// PATH first, then ~/.local/bin, then the others.
	both := func(p string) bool { return p == "/Users/x/.local/bin/herdr" || p == "/usr/local/bin/herdr" }
	p, ok = FindHerdr("", "/usr/bin", home, both)
	findOK(t, p, ok, "/Users/x/.local/bin/herdr")
}

func TestOnlyAMissingProgramIsNotFound(t *testing.T) {
	r := newFakeRunner()
	s := supervisorFor(t, r)
	r.answer(statusCmd(), runErr(supBin+": No such file or directory (os error 2)"))
	if _, err := s.Status(); !reflect.DeepEqual(err, agent.HostError{Kind: agent.HostNotFound}) {
		t.Fatalf("%#v", err)
	}
	r = newFakeRunner()
	s = supervisorFor(t, r)
	r.answer(statusCmd(), runErr(supBin+": timed out after 10s"))
	if _, err := s.Status(); !agent.IsHostError(err, agent.HostTimeout) {
		t.Fatalf("%#v", err)
	}
	r = newFakeRunner()
	s = supervisorFor(t, r)
	r.answer(statusCmd(), runErr(supBin+": Permission denied (os error 13)"))
	if _, err := s.Status(); !agent.IsHostError(err, agent.HostOther) {
		t.Fatalf("%#v", err)
	}
}

func TestStatusJSONParses(t *testing.T) {
	s, err := ParseStatus(fixture(t, "status_running.json"))
	if err != nil {
		t.Fatal(err)
	}
	proto, compat, session := uint32(17), true, supSession
	want := ServerStatus{
		Running:        true,
		ClientProtocol: 17,
		ServerProtocol: &proto,
		Compatible:     &compat,
		Socket:         "/Users/daniel/.config/herdr/sessions/plan-probe-11827/herdr.sock",
		Session:        &session,
	}
	if !reflect.DeepEqual(s, want) {
		t.Fatalf("%+v", s)
	}
}

func TestIntegrationStatusIsReadPerLine(t *testing.T) {
	if !ClaudeIntegrationCurrent(fixture(t, "integration_status.txt")) {
		t.Fatal("current")
	}
	if ClaudeIntegrationCurrent("claude: not installed (/x)\ncodex: current (v1) (/y)") {
		t.Fatal("not installed")
	}
}

func ranInstall(r *fakeRunner) bool {
	for _, argv := range r.ranArgvs() {
		if containsStr(argv, "install") {
			return true
		}
	}
	return false
}

func TestARunningCompatibleServerIsUsedAsIs(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(fixture(t, "status_running.json")))
	integrationCurrent(t, r)
	socket, err := supervisorFor(t, r).Ensure(bg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(socket, "herdr.sock") {
		t.Fatal(socket)
	}
	if r.spawnedCount() != 0 {
		t.Fatal("spawned")
	}
	for _, env := range r.ranEnvs() {
		if envGet(env, "HERDR_SESSION") != supSession {
			t.Fatal(env)
		}
	}
	if ranInstall(r) {
		t.Fatal("integration already current")
	}
}

func TestEveryAnthropicAndClaudeCodeVariableIsScrubbedButGitPushOnesStay(t *testing.T) {
	parent := pairs("ANTHROPIC_CUSTOM_HEADERS", "h", "ANTHROPIC_FOUNDRY_API_KEY", "k", "CLAUDE_CODE_OAUTH_TOKEN", "t",
		"SSH_AUTH_SOCK", "/s", "PATH", "/custom/bin", "HOME", "/Users/x")
	env, err := ServerEnv(parent, "deskpixie", "/Users/x")
	if err != nil {
		t.Fatal(err)
	}
	if got := envKeys(env); !reflect.DeepEqual(got, []string{"HERDR_SESSION", "HOME", "PATH", "SSH_AUTH_SOCK"}) {
		t.Fatal(got)
	}
	if path := envGet(env, "PATH"); !strings.HasPrefix(path, "/custom/bin:") {
		t.Fatalf("PATH comes from the parent env: %s", path)
	}
}

func TestEmptyAndDefaultSessionsAreRefused(t *testing.T) {
	for _, bad := range []string{"", "default"} {
		if _, err := ServerEnv(nil, bad, "/Users/x"); err == nil {
			t.Fatalf("%q", bad)
		}
		if _, err := NewSupervisor(supBin, pairs("HERDR_SESSION", bad), newFakeRunner()); err == nil {
			t.Fatalf("%q", bad)
		}
	}
	if _, err := NewSupervisor(supBin, nil, newFakeRunner()); err == nil {
		t.Fatal("no session at all")
	}
}

func TestAServerOfAnotherSessionIsRefused(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(strings.ReplaceAll(fixture(t, "status_running.json"), supSession, "default")))
	_, err := supervisorFor(t, r).Ensure(bg)
	if !agent.IsHostError(err, agent.HostOther) || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("%#v", err)
	}
	if r.spawnedCount() != 0 {
		t.Fatal("spawned")
	}
}

func TestARunningServerWithUnknownCompatibilityIsNotStartedAgain(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(strings.Replace(fixture(t, "status_running.json"), `"compatible":true`, `"compatible":null`, 1)))
	integrationCurrent(t, r)
	if _, err := supervisorFor(t, r).Ensure(bg); err != nil {
		t.Fatal(err)
	}
	if r.spawnedCount() != 0 {
		t.Fatal("spawned")
	}
}

func TestSessionNamesFollowTheAllowlist(t *testing.T) {
	for _, bad := range []string{"Default", "a/b", "..", "-x", "", "default", strings.Repeat("a", 49)} {
		if _, err := ServerEnv(nil, bad, "/Users/x"); err == nil {
			t.Fatalf("%q", bad)
		}
	}
	for _, good := range []string{"deskpixie", "a", "0-9", strings.Repeat("a", 48)} {
		if _, err := ServerEnv(nil, good, "/Users/x"); err != nil {
			t.Fatalf("%q: %v", good, err)
		}
	}
}

func mismatch(client, server uint32) error {
	return agent.HostError{Kind: agent.HostVersionMismatch, Client: client, Server: server}
}

func TestUnknownCompatibilityOnAnotherProtocolIsAMismatch(t *testing.T) {
	r := newFakeRunner()
	j := strings.Replace(fixture(t, "status_running.json"), `"compatible":true`, `"compatible":null`, 1)
	j = strings.Replace(j, `"protocol":17,"capabilities"`, `"protocol":18,"capabilities"`, 1)
	r.answer(statusCmd(), runOK(j))
	if _, err := supervisorFor(t, r).Ensure(bg); !reflect.DeepEqual(err, mismatch(17, 18)) {
		t.Fatalf("%#v", err)
	}
}

func TestNonJSONStatusWhileStartingIsRetried(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(notRunning(t)))
	r.answer(statusCmd(), runOK("expected value at line 1"))
	r.answer(statusCmd(), runOK(fixture(t, "status_running.json")))
	integrationCurrent(t, r)
	if _, err := supervisorFor(t, r).Ensure(bg); err != nil {
		t.Fatal(err)
	}
}

func TestAMissingIntegrationIsFlaggedNeverInstalled(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(fixture(t, "status_running.json")))
	r.answer(supBin+" integration status", runOK("claude: not installed (/x)"))
	s := supervisorFor(t, r)
	if _, err := s.Ensure(bg); err != nil {
		t.Fatal(err)
	}
	if !s.IntegrationMissing() {
		t.Fatal("not flagged")
	}
	if ranInstall(r) {
		t.Fatal("installed")
	}
}

func TestAnAbsentServerIsStartedDetachedAndWaitedFor(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(notRunning(t)))
	r.answer(statusCmd(), runOK(notRunning(t)))
	r.answer(statusCmd(), runOK(fixture(t, "status_running.json")))
	r.answer(supBin+" integration status", runOK("claude: not installed (/x)"))
	if _, err := supervisorFor(t, r).Ensure(bg); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	spawned := r.spawned
	r.mu.Unlock()
	if len(spawned) != 1 {
		t.Fatal(spawned)
	}
	if !reflect.DeepEqual(spawned[0][0], []string{supBin, "server"}) {
		t.Fatal(spawned[0][0])
	}
	if !reflect.DeepEqual(spawned[0][1], pairs("HERDR_SESSION", supSession)) {
		t.Fatal(spawned[0][1])
	}
	if ranInstall(r) {
		t.Fatal("never installs")
	}
}

func TestAServerThatNeverComesUpTimesOut(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(notRunning(t)))
	_, err := supervisorFor(t, r).Ensure(bg)
	if !reflect.DeepEqual(err, hostErr(agent.HostTimeout, "herdr server did not start")) {
		t.Fatalf("%#v", err)
	}
}

func TestAnIncompatibleServerIsAVersionMismatchAndIsLeftAlone(t *testing.T) {
	r := newFakeRunner()
	j := strings.Replace(fixture(t, "status_running.json"), `"compatible":true`, `"compatible":false`, 1)
	j = strings.Replace(j, `"protocol":17,"capabilities"`, `"protocol":18,"capabilities"`, 1)
	r.answer(statusCmd(), runOK(j))
	if _, err := supervisorFor(t, r).Ensure(bg); !reflect.DeepEqual(err, mismatch(17, 18)) {
		t.Fatalf("%#v", err)
	}
	if r.spawnedCount() != 0 {
		t.Fatal("spawned")
	}
}

func TestAClientOnAnotherProtocolIsAVersionMismatch(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runOK(strings.Replace(fixture(t, "status_running.json"), `"protocol":17,"binary"`, `"protocol":18,"binary"`, 1)))
	if _, err := supervisorFor(t, r).Ensure(bg); !reflect.DeepEqual(err, mismatch(17, 18)) {
		t.Fatalf("%#v", err)
	}
}

func TestAMissingBinaryIsNotFound(t *testing.T) {
	r := newFakeRunner()
	r.answer(statusCmd(), runErr(supBin+": No such file or directory (os error 2)"))
	if _, err := supervisorFor(t, r).Ensure(bg); !reflect.DeepEqual(err, agent.HostError{Kind: agent.HostNotFound}) {
		t.Fatalf("%#v", err)
	}
}
