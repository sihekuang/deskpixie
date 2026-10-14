package httpserver

import (
	"testing"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Ported from ai-mascot crates/agent-http/src/tokens.rs (3 tests).

func TestTokensAre64HexCharsAndResolveToTheirAgent(t *testing.T) {
	tk := NewTokens()
	a := tk.Issue("coordinator")
	if len(a) != 64 {
		t.Fatalf("len = %d", len(a))
	}
	for _, c := range a {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			t.Fatalf("not hex: %q", a)
		}
	}
	if id, ok := tk.Resolve(a); !ok || id != "coordinator" {
		t.Fatalf("resolve = %q %v", id, ok)
	}
	if _, ok := tk.Resolve("nope"); ok {
		t.Fatal("nope resolved")
	}
	if _, ok := tk.Resolve(""); ok {
		t.Fatal("empty resolved")
	}
}

func TestTokensAreTheCoresTokenIssuer(t *testing.T) {
	tk := NewTokens()
	var issuer agent.TokenIssuer = tk
	tok := issuer.Issue("focus-fix")
	if id, ok := tk.Resolve(tok); !ok || id != "focus-fix" {
		t.Fatalf("resolve = %q %v", id, ok)
	}
	issuer.Revoke("focus-fix")
	if _, ok := tk.Resolve(tok); ok {
		t.Fatal("revoked token still resolves")
	}
}

func TestReissuingReplacesAndRevokingRemoves(t *testing.T) {
	tk := NewTokens()
	old := tk.Issue("coordinator")
	nw := tk.Issue("coordinator")
	if old == nw {
		t.Fatal("reissue gave the same token")
	}
	if _, ok := tk.Resolve(old); ok {
		t.Fatal("old token still resolves")
	}
	tk.Revoke("coordinator")
	if _, ok := tk.Resolve(nw); ok {
		t.Fatal("revoked token still resolves")
	}
}

// A token kept from an earlier run (agent.keepPanesOnQuit) resolves again once restored.
func TestRestoringATokenResolvesItAndReplacesTheEarlierOne(t *testing.T) {
	tk := NewTokens()
	fresh := tk.Issue("coordinator")
	tk.Restore("coordinator", "kept-token")
	if id, ok := tk.Resolve("kept-token"); !ok || id != "coordinator" {
		t.Fatalf("resolve = %q %v", id, ok)
	}
	if _, ok := tk.Resolve(fresh); ok {
		t.Fatal("the earlier token still resolves")
	}
	if tok, ok := tk.Token("coordinator"); !ok || tok != "kept-token" {
		t.Fatalf("token = %q %v", tok, ok)
	}
	if _, ok := tk.Token("nobody"); ok {
		t.Fatal("nobody has a token")
	}
}
