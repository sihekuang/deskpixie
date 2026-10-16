package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// Issue #126: before start_worker commits to a new worker, it should notice a pix session the
// person already started on the same repo and prefer that over duplicating the work.

// fixedSecondary is a SecondaryHost whose List always returns a fixed set of sessions, for
// testing the matching/messaging logic on its own, without a full pix fake.
type fixedSecondary struct {
	*agenttest.FakeSessionHost
	sessions []agent.AgentInfo
}

func (f fixedSecondary) List(context.Context) ([]agent.AgentInfo, error) { return f.sessions, nil }

func (f fixedSecondary) Owns(name string) bool {
	for _, a := range f.sessions {
		if a.Name == name {
			return true
		}
	}
	return false
}

func TestExistingUserSessionsMatchesByRepoAndOrigin(t *testing.T) {
	matching := agent.AgentInfo{Name: "focus-claude", Origin: agent.OriginUser, User: &agent.UserSession{Repo: "focus", Description: "login bug", Where: "WebStorm"}}
	otherRepo := agent.AgentInfo{Name: "other-claude", Origin: agent.OriginUser, User: &agent.UserSession{Repo: "other", Description: "unrelated"}}
	worker := agent.AgentInfo{Name: "focus-upload", Origin: agent.OriginPixie}
	sec := fixedSecondary{FakeSessionHost: agenttest.NewFakeSessionHost(), sessions: []agent.AgentInfo{matching, otherRepo, worker}}
	host := agent.NewCompositeHost(agenttest.NewFakeSessionHost(), sec)

	got := agent.ExistingUserSessionsForTest(context.Background(), host, "focus")

	if len(got) != 1 || got[0].Name != "focus-claude" {
		t.Fatalf("%+v", got)
	}
}

func TestExistingUserSessionsNoneWithoutACompositeHost(t *testing.T) {
	host := agenttest.NewFakeSessionHost()
	if got := agent.ExistingUserSessionsForTest(context.Background(), host, "focus"); got != nil {
		t.Fatalf("%+v", got)
	}
}

func TestExistingSessionMessageNamesOneSessionByDescription(t *testing.T) {
	a := agent.AgentInfo{Name: "focus-claude", Origin: agent.OriginUser, User: &agent.UserSession{Repo: "focus", Description: "login bug", Where: "WebStorm"}}
	got := agent.ExistingSessionMessageForTest([]agent.AgentInfo{a}, "focus")
	for _, want := range []string{"focus", "login bug", "WebStorm", "focus-claude", "message_session", "override_existing_session"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestExistingSessionMessageListsEachOfSeveralSessions(t *testing.T) {
	a := agent.AgentInfo{Name: "focus-claude", Origin: agent.OriginUser, User: &agent.UserSession{Repo: "focus", Description: "login bug"}}
	b := agent.AgentInfo{Name: "focus-codex", Origin: agent.OriginUser, User: &agent.UserSession{Repo: "focus", Description: "upload fix"}}
	got := agent.ExistingSessionMessageForTest([]agent.AgentInfo{a, b}, "focus")
	for _, want := range []string{"login bug", "upload fix", "Ask the person which"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}
