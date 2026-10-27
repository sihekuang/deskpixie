package voice

import "testing"

// The claim check (#72): what Pixie's words claim it did, against the tools it called.

var allClaimTools = []string{DelegateTool, TimerToolName, MediaToolName}

func TestFindClaim(t *testing.T) {
	cases := []struct {
		said   string
		called []string
		want   string // the claimed tool with no backing call; "" = no claim
		check  bool
	}{
		// delegate: handed off / passed it / asked the agent / started a job
		{"I've handed that off to the agent.", nil, DelegateTool, false},
		{"Ive handed it off.", nil, DelegateTool, false},
		{"I’ve handed that to the agent; I'll let you know when it's done.", nil, DelegateTool, false},
		{"I've handed it to the agent, I'll let you know when it's done.", nil, DelegateTool, false},
		{"Let me hand that off.", nil, DelegateTool, false},
		{"Handing that off now.", nil, DelegateTool, false},
		{"Sure, I'll pass that to the agent.", nil, DelegateTool, false},
		{"I passed it to the agent.", nil, DelegateTool, false},
		{"I've asked the agent to look into it.", nil, DelegateTool, false},
		{"I'll ask the agent.", nil, DelegateTool, false},
		{"OK, I started a job for that.", nil, DelegateTool, false},
		{"I've kicked off a job.", nil, DelegateTool, false},
		{"I passed it to your bigger brain.", nil, DelegateTool, false},
		{"I've handed that to the coordinator.", nil, DelegateTool, false},
		{"I'll ask the coordinator.", nil, DelegateTool, false},
		{"I passed it to Claude.", nil, DelegateTool, false},
		{"I've delegated that.", nil, DelegateTool, false},
		{"I HAVE HANDED THAT OFF.", nil, DelegateTool, false},
		// #380: passing it along or telling the session, without naming the agent
		{"Alright, I'll pass that along and let you know when it's done.", nil, DelegateTool, false},
		{"I've passed it on to your session.", nil, DelegateTool, false},
		{"Sure thing, I'll tell them to create both unassigned at three points each.", nil, DelegateTool, false},
		{"I've told the session to go ahead.", nil, DelegateTool, false},
		{"Okay, I'll let them know.", nil, DelegateTool, false},
		{"I'll send that along.", nil, DelegateTool, false},
		{"I'll pass that along.", []string{"answer_job"}, "", false},
		{"I'll tell them to go ahead.", []string{DelegateTool}, "", false},
		// #95: the acknowledgement Pixie says after delegating claims a hand-off too
		{"Sure, I'm on it. I'll let you know when it's done.", nil, DelegateTool, false},
		{"On it, I'll let you know when it's done.", nil, DelegateTool, false},
		{"I'll get right on that.", nil, DelegateTool, false},
		{"Okay, I'll let you know once it's ready.", nil, DelegateTool, false},
		{"I'll get back to you.", nil, DelegateTool, false},
		{"On it, I'll let you know when it's done.", []string{DelegateTool}, "", false},
		{"Ten minute timer set, I'll let you know when it's done.", []string{TimerToolName}, "", false},
		{"I'll let you know when time's up.", []string{TimerToolName}, "", false},
		{"Their prices are on its website.", nil, "", false},
		// "let me check" promises the agent's check (any tool backs it)
		{"Let me check that.", nil, DelegateTool, true},
		{"Let me look that up.", nil, DelegateTool, true},
		{"I'll find out.", nil, DelegateTool, true},
		{"Let me check that.", []string{"get_weather"}, "", false},
		{"Let me check on the agent.", nil, "", false}, // status, not a fact check
		{"Let me check the timer.", nil, "", false},
		// backed
		{"I've handed that off to the agent.", []string{DelegateTool}, "", false},
		{"I passed your answer to the agent.", []string{"answer_job"}, "", false},
		{"I've asked the agent to stop.", []string{"jobs"}, "", false},
		// a status check or a recall is no hand-off (#133)
		{"You're right, I'll hand it off now.", []string{"jobs"}, DelegateTool, false},
		{"Let me delegate that.", []string{"jobs"}, DelegateTool, false},
		{"I've handed that off to the agent.", []string{"answer_job"}, "", false},
		// timer
		{"I've set a timer for ten minutes.", nil, TimerToolName, false},
		{"Sure, I set a ten minute timer.", nil, TimerToolName, false},
		{"Setting a timer for five minutes.", nil, TimerToolName, false},
		{"I'll set a timer for you.", nil, TimerToolName, false},
		{"I've cancelled the pasta timer.", nil, TimerToolName, false},
		{"Done, I've started your timer.", nil, TimerToolName, false},
		{"I'll remind you in twenty minutes.", nil, TimerToolName, false},
		{"I've set a timer for ten minutes.", []string{TimerToolName}, "", false},
		{"I've set a reminder.", []string{DelegateTool}, "", false},
		// media
		{"I've paused the music.", nil, MediaToolName, false},
		{"Pausing your music.", nil, MediaToolName, false},
		{"Skipped to the next song.", nil, MediaToolName, false},
		{"I'm playing your playlist now.", nil, MediaToolName, false},
		{"I turned the volume down.", nil, MediaToolName, false},
		{"I've paused the music.", []string{MediaToolName}, "", false},
		// the first unbacked claim wins; a backed one doesn't hide another
		{"I've set a timer and paused the music.", []string{TimerToolName}, MediaToolName, false},
		// not claims: offers, questions, negations, conditionals, other subjects
		{"Want me to set a timer?", nil, "", false},
		{"Should I hand that off to the agent?", nil, "", false},
		{"I haven't set a timer yet.", nil, "", false},
		{"I didn't pause the music.", nil, "", false},
		{"I can't hand that off right now.", nil, "", false},
		{"If you want, I'll set a timer.", nil, "", false},
		{"I'll set a timer once you tell me how long.", nil, "", false},
		{"How long should I set the timer for?", nil, "", false},
		{"You asked the agent about that earlier.", nil, "", false},
		{"The agent started a job an hour ago.", nil, "", false},
		{"I already handed that off earlier.", nil, "", false}, // an earlier turn
		{"I asked the agent this morning, before lunch.", nil, "", false},
		{"I'll tell you what the agent found.", nil, "", false},
		{"I'm looking forward to it.", nil, "", false},
		{"I think the agent started a job.", nil, "", false},
		{"The timer is set for ten minutes.", nil, "", false},
		{"I love this song.", nil, "", false},
		{"I'll let you know.", nil, "", false},
		{"I'll tell you when it's ready.", nil, "", false},
		{"I'll pass on that one, it's not my thing.", nil, "", false},
		{"Should I tell them to go ahead?", nil, "", false},
		{"I didn't tell them yet.", nil, "", false},
		{"Paris is the capital of France.", nil, "", false},
		{"", nil, "", false},
	}
	for _, c := range cases {
		called := map[string]bool{}
		for _, n := range c.called {
			called[n] = true
		}
		got, ok := FindClaim(c.said, called, allClaimTools)
		if c.want == "" {
			if ok {
				t.Errorf("FindClaim(%q, %v) = %+v, want none", c.said, c.called, got)
			}
			continue
		}
		if !ok || got.Tool != c.want || got.Check != c.check {
			t.Errorf("FindClaim(%q, %v) = %+v %v, want %s (check %v)", c.said, c.called, got, ok, c.want, c.check)
		}
	}
}

// Only tools that exist are covered.
func TestFindClaimOnlyForAvailableTools(t *testing.T) {
	if c, ok := FindClaim("I've paused the music.", nil, []string{DelegateTool, TimerToolName}); ok {
		t.Fatalf("no media tool, but %+v", c)
	}
	if c, ok := FindClaim("I've handed that off.", nil, []string{TimerToolName}); ok {
		t.Fatalf("no delegate tool, but %+v", c)
	}
	if c, ok := FindClaim("Let me check that.", nil, nil); ok {
		t.Fatalf("no tools, but %+v", c)
	}
}

func TestClaimNotices(t *testing.T) {
	if n := ClaimNotice(Claim{Tool: TimerToolName}); n.Text != "Coordinator: correction: no timer was set or changed. Call timer now, or ask the user how long." {
		t.Fatalf("%q", n.Text)
	}
	if n := ClaimNotice(Claim{Tool: MediaToolName}); n.Text != "Coordinator: correction: the music wasn't changed. Call media now, or tell the user it didn't happen." {
		t.Fatalf("%q", n.Text)
	}
	if n := ClaimNotice(Claim{Tool: DelegateTool}); n.Text != "" {
		t.Fatalf("a delegate claim is acted on, not corrected: %q", n.Text)
	}
}
