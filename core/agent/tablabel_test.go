package agent

import (
	"slices"
	"testing"
)

func labels(fx []Effect) []string {
	var out []string
	for _, e := range fx {
		if e.Kind == EfLabel {
			out = append(out, e.Tab.Label)
		}
	}
	return out
}

func TestShortTaskIsOneLineAndCut(t *testing.T) {
	if got := ShortTask("  fix\n the   bug ", 40); got != "fix the bug" {
		t.Fatalf("%q", got)
	}
	if got := ShortTask("abcdef ghij", 8); got != "abcdef…" {
		t.Fatalf("%q", got)
	}
}

func TestWorkerTabPutsTheNameFirst(t *testing.T) {
	tab := WorkerTab(WorkerPlan{Name: "focus-upload", Slug: "upload", RepoName: "focus", Job: job(1), Task: "Fix the flaky upload test."})
	if tab.Label != "focus-upload · job_000000000001 · Fix the flaky upload test." {
		t.Fatalf("%q", tab.Label)
	}
	want := map[string]string{"role": "worker", "job": "job_000000000001", "repo": "focus", "branch": "pixie/upload", "task": "Fix the flaky upload test."}
	for k, v := range want {
		if tab.Meta[k] != v {
			t.Fatalf("%s = %q", k, tab.Meta[k])
		}
	}
}

func TestTheCoordinatorTabShowsItsCurrentRequestThenGoesIdle(t *testing.T) {
	c := ctl()
	// Not up yet: nothing to relabel.
	if got := labels(delegate(c, 1, "summarise the issues")); got != nil {
		t.Fatal(got)
	}
	if got := labels(c.Step(CoordinatorUpInput(PaneIdle), T0)); !slices.Equal(got, []string{"coordinator · summarise the issues"}) {
		t.Fatal(got)
	}
	// Unchanged: no repeat.
	if got := labels(c.Step(TickInput(), T0)); got != nil {
		t.Fatal(got)
	}
	// A newer request, once prompted, becomes the current one.
	stop(c, `<spoken ref="job_000000000001" status="working">On it.</spoken>`, T0)
	c.Step(PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: Coordinator, Status: PaneIdle}), T0)
	var got []string
	got = append(got, labels(delegate(c, 2, "rename the tabs"))...)
	got = append(got, labels(c.Step(PaneInput(PaneEvent{Kind: PaneEventStatus, Agent: Coordinator, Status: PaneIdle}), T0+10_000))...)
	if !slices.Equal(got, []string{"coordinator · rename the tabs"}) {
		t.Fatal(got)
	}
	// Job 2 ends: back to job 1, still open; then idle once none is.
	if got := labels(c.Step(CancelInput(job(2)), T0)); !slices.Equal(got, []string{"coordinator · summarise the issues"}) {
		t.Fatal(got)
	}
	if got := labels(c.Step(CancelInput(job(1)), T0)); !slices.Equal(got, []string{"coordinator"}) {
		t.Fatal(got)
	}
	// A new coordinator pane is labelled again once up.
	c.Step(PaneInput(PaneEvent{Kind: PaneEventExited, Agent: Coordinator}), T0)
	if got := labels(c.Step(CoordinatorUpInput(PaneIdle), T0)); !slices.Equal(got, []string{"coordinator"}) {
		t.Fatal(got)
	}
}
