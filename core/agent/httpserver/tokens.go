package httpserver

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"sync"

	"github.com/sihekuang/deskpixie/core/agent"
)

// Tokens is per-agent bearer tokens: 32 random bytes, held in memory only, never logged
// (Rust: agent-http tokens.rs). Safe for concurrent use.
type Tokens struct {
	mu sync.Mutex
	// byToken maps token → identity (an agent name such as "coordinator").
	byToken map[string]string
}

var _ agent.TokenIssuer = (*Tokens)(nil)

func NewTokens() *Tokens { return &Tokens{byToken: map[string]string{}} }

// Issue returns a fresh token for identity, replacing any earlier one.
func (t *Tokens) Issue(identity string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("the OS random source works: " + err.Error())
	}
	token := hex.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	for tok, id := range t.byToken {
		if id == identity {
			delete(t.byToken, tok)
		}
	}
	t.byToken[token] = identity
	return token
}

// Restore makes token identity's again, replacing any earlier one: a token kept from an
// earlier run for an agent left running over the quit (agent.keepPanesOnQuit).
func (t *Tokens) Restore(identity, token string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for tok, id := range t.byToken {
		if id == identity {
			delete(t.byToken, tok)
		}
	}
	t.byToken[token] = identity
}

// Token is identity's current token, if it has one.
func (t *Tokens) Token(identity string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for tok, id := range t.byToken {
		if id == identity {
			return tok, true
		}
	}
	return "", false
}

// Resolve says which agent holds bearer. It compares in constant time per candidate.
func (t *Tokens) Resolve(bearer string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	found, ok := "", false
	for tok, id := range t.byToken {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(bearer)) == 1 {
			found, ok = id, true
		}
	}
	return found, ok
}

// Revoke drops identity's token.
func (t *Tokens) Revoke(identity string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for tok, id := range t.byToken {
		if id == identity {
			delete(t.byToken, tok)
		}
	}
}
