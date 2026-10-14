package agent

import (
	"encoding/json"
	"testing"
)

// Ported from crates/agent-core/src/types.rs (6 tests).

func TestJobIdsAreJobPlus12Hex(t *testing.T) {
	if got := JobIDFromBits(0xab); got != "job_0000000000ab" {
		t.Fatalf("got %q", got)
	}
	if got := JobIDFromBits(^uint64(0)); got != "job_ffffffffffff" {
		t.Fatalf("got %q", got)
	}
	if _, ok := ParseJobID("job_0123456789ab"); !ok {
		t.Fatal("valid id rejected")
	}
	for _, bad := range []string{"job_0123456789AB", "job_123", "jab_0123456789ab", "job_0123456789abc", "job_01234567890g"} {
		if _, ok := ParseJobID(bad); ok {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestJobIdsRoundTripThroughJSONAndRejectJunk(t *testing.T) {
	id := JobIDFromBits(7)
	b, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"job_000000000007"` {
		t.Fatalf("got %s", b)
	}
	var back JobID
	if err := json.Unmarshal(b, &back); err != nil || back != id {
		t.Fatalf("round trip: %v %q", err, back)
	}
	if err := json.Unmarshal([]byte(`"../etc"`), &back); err == nil {
		t.Fatal("junk accepted")
	}
}

func TestOpenStatuses(t *testing.T) {
	if !(JobDelegated.IsOpen() && JobWorking.IsOpen() && JobQuestion.IsOpen()) {
		t.Fatal("open statuses")
	}
	if JobDone.IsOpen() || JobFailed.IsOpen() || JobCancelled.IsOpen() {
		t.Fatal("closed statuses")
	}
	b, _ := json.Marshal(JobQuestion)
	if string(b) != `"question"` {
		t.Fatalf("got %s", b)
	}
}

func TestAgentKindsParseAndSerializeLowercase(t *testing.T) {
	if k, ok := ParseAgentKind(" Codex "); !ok || k != Codex {
		t.Fatal("Codex")
	}
	if k, ok := ParseAgentKind("claude"); !ok || k != Claude {
		t.Fatal("claude")
	}
	if _, ok := ParseAgentKind("gpt"); ok {
		t.Fatal("gpt")
	}
	b, _ := json.Marshal(Codex)
	if string(b) != `"codex"` {
		t.Fatalf("got %s", b)
	}
}

func TestWorkerRowsRoundTripAndOldJobsHaveNone(t *testing.T) {
	row := WorkerRow{Name: "focus-fix", Kind: Claude, Repo: "focus", JobID: JobIDFromBits(1), Status: WorkerRunning}
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if m["status"] != "running" || m["kind"] != "claude" {
		t.Fatalf("got %s", b)
	}
	var back WorkerRow
	if err := json.Unmarshal(b, &back); err != nil || back != row {
		t.Fatalf("round trip: %v %+v", err, back)
	}
	old := `{"id":"job_000000000001","task":"t","status":"done","created":1,"updated":2}`
	var j Job
	if err := json.Unmarshal([]byte(old), &j); err != nil || len(j.Workers) != 0 {
		t.Fatalf("old job: %v %+v", err, j)
	}
	if !(WorkerStarting.IsLive() && WorkerBlocked.IsLive()) {
		t.Fatal("live")
	}
	if WorkerStopped.IsLive() || WorkerFailed.IsLive() {
		t.Fatal("not live")
	}
}

func TestPaneStatusUsesHerdrNames(t *testing.T) {
	cases := map[string]PaneStatus{"idle": PaneIdle, "working": PaneWorking, "blocked": PaneBlocked, "done": PaneDone, "weird": PaneUnknown}
	for s, want := range cases {
		if got := ParsePaneStatus(s); got != want {
			t.Errorf("%s: got %v", s, got)
		}
	}
	if !(PaneIdle.IsReady() && PaneDone.IsReady()) {
		t.Fatal("ready")
	}
	if PaneWorking.IsReady() || PaneBlocked.IsReady() {
		t.Fatal("not ready")
	}
}
