package agent_test

import (
	"strings"
	"testing"
	"testing/synctest"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
	"github.com/sihekuang/deskpixie/core/voice"
)

// #72: what Pixie says about job status comes from the job table, through the jobs tool and the
// agent's own lines, never from Pixie's memory.

func TestBitsJobStatusComesFromTheJobTable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.voice.SetOpen(true)
		r.h.Voice(VoiceSignal{Kind: voice.SigOpened})
		delegate(t, r, "fix the build")
		delegate(t, r, "count issues")
		delegate(t, r, "deploy the site")
		settle()
		stopHook(r, `<spoken ref="job_000000000002" status="done">There are 12.</spoken>`+
			`<spoken ref="job_000000000003" status="failed">The deploy key is missing.</spoken>`)
		settle()

		want := map[string]string{
			"job_000000000001": "working",
			"job_000000000002": "done",
			"job_000000000003": "failed",
		}
		// The table itself.
		table := map[string]string{}
		for _, v := range r.h.JobStatus() {
			table[v.JobID] = v.Status.String()
		}
		// What the jobs tool tells Pixie.
		out := invoke(t, Jobs{H: r.h}, `{"action":"status"}`).(map[string]any)
		told := map[string]string{}
		for _, j := range out["jobs"].([]any) {
			m := j.(map[string]any)
			told[m["job_id"].(string)] = m["status"].(string)
		}
		for id, status := range want {
			if table[id] != status || told[id] != status {
				t.Errorf("%s: want %s, table %s, jobs tool %s", id, status, table[id], told[id])
			}
		}
		if len(told) != len(table) {
			t.Errorf("jobs tool %v, table %v", told, table)
		}

		// The lines Pixie hears say exactly what the table says: a reply for the done and the
		// failed job, nothing for the one still working.
		var replies []string
		for _, c := range r.voice.Calls() {
			if c.Kind == agenttest.CallInject {
				replies = append(replies, c.Text)
			}
		}
		for _, line := range []string{"Coordinator reply: There are 12.", "Coordinator reply: The deploy key is missing."} {
			if !hasCall(r.voice.Calls(), agenttest.InjectCall(line)) {
				t.Errorf("Pixie wasn't told %q: %q", line, replies)
			}
		}
		if len(replies) != 2 {
			t.Errorf("Pixie was told more than the table says: %q", replies)
		}
	})
}

// The persona sends every status question to the jobs tool, and keeps memory out of it.
func TestThePersonaTakesJobStatusOnlyFromTheJobsTool(t *testing.T) {
	for _, phrase := range []string{
		`call jobs with action "status" and answer from it`,
		"Job status comes only from the jobs tool and Coordinator lines",
		`never from memory or "Recent work with the coordinator"`,
		"Never say a job finished unless a Coordinator reply said so.",
	} {
		if !strings.Contains(DelegationPersona, phrase) {
			t.Errorf("the delegation persona doesn't say %q", phrase)
		}
	}
}
