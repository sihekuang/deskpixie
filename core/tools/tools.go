// Package tools holds the local voice tools (time, weather, calculate, timer, media) and the
// session facts, ported from the Rust voice-tools crate. Each tool is a plain struct; the app
// adapts them to voice.Tool. Each external boundary (clock, place lookup, weather, shell) is an
// interface so tests inject fakes and no test touches the network.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // zone names must resolve even where the OS has no zoneinfo
)

// Tool is the shape every local tool has; P14 adapts it to voice.Tool.
// Failures are reported in-band as {"error": "..."} output; err is reserved for
// programming errors and is always nil from these tools.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) (output json.RawMessage, endConversation bool, err error)
}

// SessionFacts supplies text appended to the model's instructions when a conversation connects.
type SessionFacts interface{ Facts() string }

// LookupTimeout is the longest a network lookup may take before the tool answers {"error":"timeout"}.
const LookupTimeout = 3 * time.Second

func reply(v any) (json.RawMessage, bool, error) {
	b, err := json.Marshal(v)
	return b, false, err
}

func fail(msg string) (json.RawMessage, bool, error) { return reply(map[string]any{"error": msg}) }

type args map[string]any

// parseArgs decodes a JSON object, keeping numbers exact (json.Number). Bad input is an empty set.
func parseArgs(raw json.RawMessage) args {
	out := args{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return out
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil || out == nil {
		return args{}
	}
	return out
}

// text is a trimmed, non-empty string argument.
func (a args) text(key string) (string, bool) {
	s, ok := a[key].(string)
	s = strings.TrimSpace(s)
	return s, ok && s != ""
}

// float is a numeric argument (integer or fractional).
func (a args) float(key string) (float64, bool) {
	n, ok := a[key].(json.Number)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(string(n), 64)
	return f, err == nil
}

// uint is a non-negative integer written without a fraction (60, not 60.0), like serde's as_u64.
func (a args) uint(key string) (uint64, bool) {
	n, ok := a[key].(json.Number)
	if !ok {
		return 0, false
	}
	u, err := strconv.ParseUint(string(n), 10, 64)
	return u, err == nil
}

func (a args) str(key string) (string, bool) { s, ok := a[key].(string); return s, ok }
