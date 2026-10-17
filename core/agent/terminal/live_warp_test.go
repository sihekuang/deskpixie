//go:build live

package terminal

// Opt-in live check that a click shows the Warp tab attached to the session (#115):
//
//	go test -tags live -run LiveWarp -v -timeout 5m ./agent/terminal/
//
// Needs Warp; it takes the front of the screen while it runs. A Warp tab is attached to the
// herdr smoke session ("deskpixie-smoke") through the app's own Tab Config (saved and put back
// afterwards), then buried behind a new tab and Warp sent to the back. A second Open must show
// that tab again by its WARP_FOCUS_URL: no window-title raise, no new tab. The attach ends at
// the end, which closes its tab.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/herdr"
	"github.com/sihekuang/deskpixie/core/herdr/smoketest"
)

// recordingRunner runs commands for real and records them.
type recordingRunner struct {
	agent.CommandRunner
	mu  sync.Mutex
	ran []string
}

func (r *recordingRunner) Run(argv []string, env []agent.EnvVar) (agent.CommandOutput, error) {
	r.mu.Lock()
	r.ran = append(r.ran, strings.Join(argv, " "))
	r.mu.Unlock()
	return r.CommandRunner.Run(argv, env)
}

func (r *recordingRunner) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ran := r.ran
	r.ran = nil
	return ran
}

func frontApp() string {
	asn, _ := exec.Command("/usr/bin/lsappinfo", "front").Output()
	out, _ := exec.Command("/usr/bin/lsappinfo", "info", "-only", "bundleid", strings.TrimSpace(string(asn))).Output()
	if _, id, ok := strings.Cut(string(out), `bundleID="`); ok {
		id, _, _ = strings.Cut(id, `"`)
		return id
	}
	return ""
}

// warpTitle is Warp's front window title (System Events: needs Accessibility; "" without).
func warpTitle() string {
	out, err := exec.Command("/usr/bin/osascript", "-e",
		`tell application "System Events" to get name of front window of (first process whose bundle identifier is "dev.warp.Warp-Stable")`).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func waitUntil(t *testing.T, what string, within time.Duration, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(within); !ok(); time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not within %v", what, within)
		}
	}
}

// executeLive performs exactly the primitives the Swift app will perform, for this live test's
// own verification (there is no Swift test target in this repo). It reports which rung
// succeeded, so the test can check it never had to fall further than it expects.
func executeLive(t *testing.T, plan agent.FocusPlan) string {
	t.Helper()
	for _, u := range plan.FocusURLs {
		if exec.Command("/usr/bin/open", u).Run() == nil {
			return "focus_url"
		}
	}
	if len(plan.RaiseScript) > 0 {
		var args []string
		for _, s := range plan.RaiseScript {
			args = append(args, "-e", s)
		}
		if out, err := exec.Command("/usr/bin/osascript", args...).Output(); err == nil && strings.TrimSpace(string(out)) == "ok" {
			return "raise"
		}
	}
	for _, pid := range plan.KillPIDs {
		exec.Command("/bin/kill", "-HUP", strconv.Itoa(pid)).Run()
	}
	if plan.NewTerminal != nil && plan.NewTerminal.Kind == "url" {
		exec.Command("/usr/bin/open", plan.NewTerminal.URL).Run()
	}
	return "new_terminal"
}

func TestLiveWarpShowsTheAttachedTabByItsFocusURL(t *testing.T) {
	if _, err := os.Stat("/Applications/Warp.app"); err != nil {
		t.Skip("Warp not installed")
	}
	sm := smoketest.Smoke(t)
	host := herdr.NewHost(smoketest.SmokeSession, sm.Home, sm.Supervisor(t))
	t.Cleanup(host.Shutdown)
	if err := host.EnsureSession(context.Background()); err != nil {
		t.Fatalf("session: %v", err)
	}

	// The app's Tab Config, put back as it was.
	dir := filepath.Join(sm.Home, ".warp", "tab_configs")
	cfg := filepath.Join(dir, WarpStem+".toml")
	if was, err := os.ReadFile(cfg); err == nil {
		t.Cleanup(func() { os.WriteFile(cfg, was, 0o644) })
	} else {
		t.Cleanup(func() { os.Remove(cfg) })
	}

	r := &recordingRunner{CommandRunner: sm.Runner}
	l := Reuse(NewWarpPlanner(dir), r)
	attach := agent.Attach{Argv: host.AttachCommand(), Dir: sm.Home}

	// 1. Nothing attached: a new tab.
	if _, _, _, ok := l.attached(attach.Argv); ok {
		t.Fatal("something is already attached to the smoke session")
	}
	plan, err := l.Plan(attach)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	executeLive(t, plan)
	var clients []int
	waitUntil(t, "a Warp tab attached", 30*time.Second, func() bool {
		var ok bool
		_, _, clients, ok = l.attached(attach.Argv)
		return ok
	})
	t.Cleanup(func() {
		for _, pid := range clients {
			exec.Command("/bin/kill", "-HUP", strconv.Itoa(pid)).Run()
		}
	})
	t.Logf("ok   attached in a new Warp tab (client %v); window %q", clients, warpTitle())
	r.take()

	// 2. Bury it behind a new tab, then put another app in front.
	// The other tab is a Tab Config of the test's own, ended (closing its tab) at the end.
	mark := "deskpixie-smoke-bury-" + strconv.Itoa(os.Getpid())
	bury := filepath.Join(dir, "deskpixie_smoke_bury.toml")
	toml := "name = \"Bury\"\ntitle = \"Bury\"\n\n[[panes]]\nid = \"main\"\ntype = \"terminal\"\ncommands = [\"exec -a " + mark + " sleep 60\"]\n"
	if err := os.WriteFile(bury, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Warp may run the command only after its shell is up: wait for it.
		for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(500 * time.Millisecond) {
			if exec.Command("/usr/bin/pkill", "-f", mark).Run() == nil {
				break
			}
		}
		os.Remove(bury)
	})
	exec.Command("/usr/bin/open", "warp://tab_config/deskpixie_smoke_bury").Run()
	buried := ""
	waitUntil(t, "the other tab in front", 15*time.Second, func() bool { buried = warpTitle(); return buried == "Bury" })
	t.Logf("     buried behind another tab; window %q", buried)
	exec.Command("/usr/bin/open", "-a", "Finder").Run()
	waitUntil(t, "Finder in front", 10*time.Second, func() bool { return frontApp() == "com.apple.finder" })

	// 3. The click: the attached tab, by its focus URL.
	plan, err = l.Plan(attach)
	if err != nil {
		t.Fatalf("plan again: %v", err)
	}
	t.Logf("     ran %q", r.take())
	if len(plan.FocusURLs) == 0 {
		t.Fatal("no focus URL in the plan")
	}
	if rung := executeLive(t, plan); rung != "focus_url" {
		t.Fatalf("fell back to %q instead of the focus url", rung)
	}
	waitUntil(t, "Warp in front", 10*time.Second, func() bool { return frontApp() == "dev.warp.Warp-Stable" })
	if _, _, now, _ := l.attached(attach.Argv); len(now) != len(clients) {
		t.Fatalf("clients %v, were %v: a tab was replaced", now, clients)
	}
	t.Log("ok   Warp in front by the focus URL, the same tab still attached")
	waitUntil(t, "the attached tab shown", 5*time.Second, func() bool { return warpTitle() == "DeskPixie" })
	t.Logf("ok   the attached tab is the one shown (window %q, was %q)", warpTitle(), buried)
}
