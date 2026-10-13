package herdr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// fixture reads a captured Herdr 0.7.5 shape from testdata (Rust: tests/fixtures).
func fixture(t testing.TB, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// jsonValue round-trips v through JSON so maps, slices and structs compare as serde Values do.
func jsonValue(t testing.TB, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// mustJSON parses a JSON literal (Rust's json! macro in the tests).
func mustJSON(t testing.TB, s string) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		t.Fatalf("bad JSON literal %s: %v", s, err)
	}
	return out
}

func assertJSON(t testing.TB, got any, want string) {
	t.Helper()
	g, w := jsonValue(t, got), mustJSON(t, want)
	if !reflect.DeepEqual(g, w) {
		gb, _ := json.Marshal(g)
		t.Fatalf("got %s\nwant %s", gb, want)
	}
}

// fakeRunner is agent-core's FakeRunner (fakes.rs): answers per joined argv, the last one
// repeating; records runs and detached spawns.
type fakeRunner struct {
	mu      sync.Mutex
	ran     [][2]any // {argv []string, env []agent.EnvVar}
	spawned [][2]any
	answers map[string][]runAnswer
}

type runAnswer struct {
	out agent.CommandOutput
	err string
}

func newFakeRunner() *fakeRunner { return &fakeRunner{answers: map[string][]runAnswer{}} }

func runOK(stdout string) runAnswer { return runAnswer{out: agent.CommandOutput{Stdout: stdout}} }
func runErr(msg string) runAnswer   { return runAnswer{err: msg} }

func (r *fakeRunner) answer(argv string, a runAnswer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[argv] = append(r.answers[argv], a)
}

func (r *fakeRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, [2]any{append([]string(nil), argv...), append([]agent.EnvVar(nil), env...)})
	key := strings.Join(argv, " ")
	q := r.answers[key]
	var a runAnswer
	switch {
	case len(q) > 1:
		a = q[0]
		r.answers[key] = q[1:]
	case len(q) == 1:
		a = q[0]
	default:
		return agent.CommandOutput{}, fmt.Errorf("no scripted answer for %q", key)
	}
	if a.err != "" {
		return agent.CommandOutput{}, fmt.Errorf("%s", a.err)
	}
	return a.out, nil
}

func (r *fakeRunner) RunBy(argv []string, env []agent.EnvVar, deadline time.Time) (agent.CommandOutput, error) {
	return agent.DefaultRunBy(r, argv, env, deadline)
}

func (r *fakeRunner) SpawnDetached(argv []string, env []agent.EnvVar) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.spawned = append(r.spawned, [2]any{append([]string(nil), argv...), append([]agent.EnvVar(nil), env...)})
	return nil
}

func (r *fakeRunner) spawnedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.spawned)
}

func (r *fakeRunner) ranArgvs() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]string
	for _, x := range r.ran {
		out = append(out, x[0].([]string))
	}
	return out
}

func (r *fakeRunner) ranEnvs() [][]agent.EnvVar {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]agent.EnvVar
	for _, x := range r.ran {
		out = append(out, x[1].([]agent.EnvVar))
	}
	return out
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func hostErr(kind agent.HostErrorKind, detail string) error {
	return agent.HostError{Kind: kind, Detail: detail}
}

// scriptedTransport (client.rs tests) answers each method from a queue of captured response
// lines (the last repeats) and records requests.
type scriptedTransport struct {
	mu      sync.Mutex
	answers map[string][]string
	sent    []sentReq
}

type sentReq struct {
	method string
	params any
}

func newScriptedTransport() *scriptedTransport {
	return &scriptedTransport{answers: map[string][]string{}}
}

func (s *scriptedTransport) answer(method, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[method] = append(s.answers[method], strings.TrimSpace(line))
}

func (s *scriptedTransport) sentFor(method string) []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []any
	for _, r := range s.sent {
		if r.method == method {
			out = append(out, r.params)
		}
	}
	return out
}

func (s *scriptedTransport) Request(_ context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, sentReq{method, params})
	q, ok := s.answers[method]
	if !ok || len(q) == 0 {
		return nil, RPCError{Code: "unscripted", Message: method}
	}
	line := q[0]
	if len(q) > 1 {
		s.answers[method] = q[1:]
	}
	return ParseResponse(line)
}
