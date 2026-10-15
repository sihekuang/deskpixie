package agentapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/settings"
)

// Port of src-tauri/src/agent/registry.rs tests (8).

func noHint(string) {}

func TestRegistriesNameTheShippedAdapters(t *testing.T) {
	if got := slices.Sorted(keys(SessionHosts())); !reflect.DeepEqual(got, []string{"herdr", "tmux"}) {
		t.Fatal(got)
	}
	if got := slices.Sorted(keys(Terminals())); !reflect.DeepEqual(got, []string{"iterm", "warp"}) {
		t.Fatal(got)
	}
	runner := &agenttest.FakeRunner{}
	if n := Terminals()["warp"](runner, "/Users/x").Name(); n != "warp" {
		t.Fatal(n)
	}
	if n := Terminals()["iterm"](runner, "/Users/x").Name(); n != "iterm" {
		t.Fatal(n)
	}
}

func keys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func TestAMissingHerdrIsNotFound(t *testing.T) {
	s := settings.Default().Agent
	p := "/definitely/not/herdr"
	s.HerdrPath = &p
	_, err := SessionHosts()["herdr"](HostDeps{Settings: s, Home: "/nonexistent-home", Runner: &agenttest.FakeRunner{}, Hint: noHint})
	if !agent.IsHostError(err, agent.HostNotFound) {
		t.Fatal(err)
	}
}

func TestAMissingTmuxIsNotFoundNamingTmux(t *testing.T) {
	s := settings.Default().Agent
	s.SessionHost = "tmux"
	p := "/definitely/not/tmux"
	s.TmuxPath = &p
	_, err := SessionHosts()["tmux"](HostDeps{Settings: s, Home: "/nonexistent-home", Runner: &agenttest.FakeRunner{}, Hint: noHint})
	if !agent.IsHostError(err, agent.HostNotFound) || HostProblem(err) != "Coordinator: tmux not found" {
		t.Fatal(err)
	}
}

// The tmux host runs the found tmux on its own socket with the scrubbed environment, and is
// not wrapped (no integration hint: its hooks are DeskPixie's own).
func TestTheTmuxHostRunsTheConfiguredTmuxOnItsOwnSocket(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "tmux")
	if err := os.WriteFile(bin, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	s := settings.Default().Agent
	s.SessionHost, s.TmuxPath = "tmux", &bin
	runner := &agenttest.FakeRunner{}
	runner.Answer(bin+" -V", agenttest.Ok("tmux 3.5a\n"), nil)
	runner.Answer(bin+" -L deskpixie has-session -t =deskpixie", agenttest.Ok(""), nil)
	hinted := false
	h, err := SessionHosts()["tmux"](HostDeps{Settings: s, Home: dir, Runner: runner, Hint: func(string) { hinted = true },
		Environ: []string{"PATH=/usr/bin", "ANTHROPIC_API_KEY=k", "TMUX=/tmp/x", "LANG=C"}})
	if err != nil {
		t.Fatal(err)
	}
	defer shutdownHost(h)
	if h.Name() != "tmux" {
		t.Fatal(h.Name())
	}
	if _, wrapped := h.(*IntegrationCheck); wrapped {
		t.Fatal("no integration check for tmux")
	}
	if err := h.EnsureSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hinted {
		t.Fatal("no hint")
	}
	ran := runner.Ran()
	if len(ran) != 2 || !reflect.DeepEqual(ran[1].Argv, []string{bin, "-L", "deskpixie", "has-session", "-t", "=deskpixie"}) {
		t.Fatal(ran)
	}
	want := []agent.EnvVar{{Key: "LANG", Value: "C"}, {Key: "PATH", Value: "/usr/bin:" + filepath.Join(dir, ".local/bin") + ":/opt/homebrew/bin:/usr/local/bin"}}
	if !reflect.DeepEqual(ran[1].Env, want) {
		t.Fatal(ran[1].Env)
	}
	if got := h.AttachCommand(); !reflect.DeepEqual(got, []string{bin, "-L", "deskpixie", "attach", "-t", "deskpixie"}) {
		t.Fatal(got)
	}
	s.Session = "default"
	if _, err := SessionHosts()["tmux"](HostDeps{Settings: s, Home: dir, Runner: runner, Hint: noHint}); !agent.IsHostError(err, agent.HostOther) {
		t.Fatal(err)
	}
}

func TestASharedOrMalformedSessionNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "herdr")
	if err := os.WriteFile(bin, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &agenttest.FakeRunner{}
	for _, session := range []string{"default", "", "Ai Pixie"} {
		s := settings.Default().Agent
		s.HerdrPath = &bin
		s.Session = session
		_, err := SessionHosts()["herdr"](HostDeps{Settings: s, Home: dir, Runner: runner, Hint: noHint})
		if !agent.IsHostError(err, agent.HostOther) {
			t.Errorf("%q: %v", session, err)
		}
	}
	if len(runner.Ran()) != 0 || len(runner.Spawned()) != 0 {
		t.Fatal("nothing ran")
	}
}

func TestUnavailableReportsItsError(t *testing.T) {
	u := Unavailable{Err: agent.HostError{Kind: agent.HostNotFound}}
	if err := u.EnsureSession(context.Background()); !agent.IsHostError(err, agent.HostNotFound) {
		t.Fatal(err)
	}
	err := u.Close(context.Background(), "coordinator")
	var he agent.HostError
	if !errors.As(err, &he) || he != (agent.HostError{Kind: agent.HostNoAgent, Detail: "coordinator"}) {
		t.Fatal(err)
	}
}

type checkProbe struct {
	asked  atomic.Int32
	mu     sync.Mutex
	posted []string
}

func (p *checkProbe) Posted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.posted)
}

// checkedOver is an IntegrationCheck over inner whose host reports the Claude integration
// missing; it returns the check and a probe counting how often the host was asked and the
// hints posted.
func checkedOver(inner agent.SessionHost, missing bool) (*IntegrationCheck, *checkProbe) {
	p := &checkProbe{}
	return NewIntegrationCheck(inner, func() bool {
		p.asked.Add(1)
		return missing
	}, func(line string) {
		p.mu.Lock()
		p.posted = append(p.posted, line)
		p.mu.Unlock()
	}), p
}

func TestAMissingClaudeIntegrationPostsTheHintOnce(t *testing.T) {
	host, p := checkedOver(agenttest.NewFakeSessionHost(), true)
	ctx := context.Background()
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatal(err)
	}
	if got := p.Posted(); !reflect.DeepEqual(got, []string{IntegrationHint}) {
		t.Fatal("posted once:", got)
	}
	if IntegrationHint != "Coordinator: run `herdr integration install claude`" {
		t.Fatal(IntegrationHint)
	}
	if p.asked.Load() != 1 {
		t.Fatal("the host's own check, read once")
	}
}

func TestACurrentIntegrationPostsNothingAndDelegationStillWorks(t *testing.T) {
	host, p := checkedOver(agenttest.NewFakeSessionHost(), false)
	ctx := context.Background()
	if err := host.EnsureSession(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := host.StartAgent(ctx, agent.AgentSpec{Name: "coordinator", Kind: agent.Claude, Cwd: "/w"})
	if err != nil || info.Name != "coordinator" {
		t.Fatal(info, err)
	}
	if len(p.Posted()) != 0 {
		t.Fatal(p.Posted())
	}
}

// replacer overrides only ReplaceAgent, so a wrapper that skips it is caught.
type replacer struct{ *agenttest.FakeSessionHost }

func (replacer) Name() string { return "replacer" }
func (replacer) ReplaceAgent(context.Context, agent.AgentSpec) (agent.AgentInfo, error) {
	return agent.AgentInfo{}, agent.HostError{Kind: agent.HostOther, Detail: "the host's own replace"}
}

func TestTheWrapperForwardsReplaceAgentToTheHost(t *testing.T) {
	host, _ := checkedOver(replacer{agenttest.NewFakeSessionHost()}, false)
	_, err := host.ReplaceAgent(context.Background(), agent.AgentSpec{Name: "coordinator", Kind: agent.Claude, Cwd: "/w"})
	if err == nil || err.Error() != "the host's own replace" {
		t.Fatal(err)
	}
}

func TestAFailedSessionSkipsTheCheck(t *testing.T) {
	fake := agenttest.NewFakeSessionHost()
	fake.FailEnsure(agent.HostError{Kind: agent.HostNotFound})
	host, p := checkedOver(fake, true)
	if err := host.EnsureSession(context.Background()); !agent.IsHostError(err, agent.HostNotFound) {
		t.Fatal(err)
	}
	if p.asked.Load() != 0 || len(p.Posted()) != 0 {
		t.Fatal("checked after a failed session")
	}
}

// The roster lists passively through agent.ListRunning; a wrapper that dropped ListRunning
// would silently turn that into an active List, which starts `herdr server`.
func TestIntegrationCheckForwardsThePassiveList(t *testing.T) {
	spy := &activeListSpy{FakeSessionHost: agenttest.NewFakeSessionHost()}
	host, _ := checkedOver(spy, true)
	if _, ok := any(host).(agent.PassiveLister); !ok {
		t.Fatal("IntegrationCheck is not an agent.PassiveLister")
	}
	if _, err := agent.ListRunning(context.Background(), host); err != nil {
		t.Fatal(err)
	}
	if spy.active.Load() != 0 || spy.passive.Load() != 1 {
		t.Fatalf("active List %d, ListRunning %d; want 0 and 1", spy.active.Load(), spy.passive.Load())
	}
}
