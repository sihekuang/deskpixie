//go:build live

package terminal

// Opt-in live check of #148 against the pix sessions running right now (start some first, in
// Warp and/or iTerm):
//
//	go test -tags live -run LiveSession -v ./agent/terminal/
//
// It only plans (reads ps); it opens nothing.

import (
	"os/exec"
	"strconv"
	"strings"
	"testing"

	agentproc "github.com/sihekuang/deskpixie/core/agent/proc"
)

func TestLiveSessionPlannerFindsEachRunningPixSessionsTab(t *testing.T) {
	out, err := exec.Command(psBin, "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	isPix := func(argv0 string) bool { return argv0 == "pix" || strings.HasSuffix(argv0, "/pix") }
	pixes := map[string]bool{}
	var agents []int
	rows := strings.Split(string(out), "\n")
	for _, row := range rows {
		if f := strings.Fields(row); len(f) >= 4 && isPix(f[2]) && f[3] == "claude" {
			pixes[f[0]] = true
		}
	}
	for _, row := range rows {
		if f := strings.Fields(row); len(f) >= 3 && pixes[f[1]] && !isPix(f[2]) {
			pid, _ := strconv.Atoi(f[0])
			agents = append(agents, pid)
		}
	}
	if len(agents) == 0 {
		t.Skip("no pix claude session running")
	}
	p := NewSessionPlanner(agentproc.ProcessRunner{})
	for _, pid := range agents {
		env, _ := exec.Command(psBin, "-E", "-ww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		plan, err := p.PlanSession(pid)
		if err != nil {
			t.Errorf("pid %d: %v", pid, err)
			continue
		}
		switch {
		case strings.Contains(string(env), "WARP_FOCUS_URL=warp://session/"):
			if len(plan.FocusURLs) != 1 {
				t.Errorf("pid %d runs in Warp but the plan is %+v", pid, plan)
			}
		case strings.Contains(string(env), "ITERM_SESSION_ID="):
			if len(plan.ItermSelect) == 0 {
				t.Errorf("pid %d runs in iTerm but the plan is %+v", pid, plan)
			}
		}
		t.Logf("pid %d: urls=%v iterm=%v app=%q", pid, plan.FocusURLs, len(plan.ItermSelect) > 0, plan.ActivateApp)
	}
}
