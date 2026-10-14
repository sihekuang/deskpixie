package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/sihekuang/deskpixie/core/agent"
)

// HerdrAgent is an agent as agent.list / agent.get / agent.start report it (shapes captured
// from Herdr 0.7.5, protocol 17). Rust's Option<String> fields are "" when absent.
type HerdrAgent struct {
	Name   string
	PaneID string
	// Agent is the detected agent ("claude"). "" while the agent is launching (LaunchPending)
	// and once its process has exited, though the pane and its name stay.
	Agent  string
	Status agent.PaneStatus
	Cwd    string
	// InteractiveReady: the agent's TUI is ready for input (absent while launching).
	InteractiveReady bool
	// LaunchPending: agent.start typed the command and the agent is not detected yet.
	LaunchPending bool
}

// ParseHerdrAgent reads an agent object; false without a string pane_id.
func ParseHerdrAgent(raw json.RawMessage) (HerdrAgent, bool) {
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return HerdrAgent{}, false
	}
	return parseAgentMap(v)
}

func parseAgentMap(v map[string]any) (HerdrAgent, bool) {
	pane, ok := v["pane_id"].(string)
	if !ok {
		return HerdrAgent{}, false
	}
	s := func(k string) string { x, _ := v[k].(string); return x }
	b := func(k string) bool { x, _ := v[k].(bool); return x }
	status := "unknown"
	if x, ok := v["agent_status"].(string); ok {
		status = x
	}
	return HerdrAgent{
		Name:             s("name"),
		PaneID:           pane,
		Agent:            s("agent"),
		Status:           agent.ParsePaneStatus(status),
		Cwd:              s("cwd"),
		InteractiveReady: b("interactive_ready"),
		LaunchPending:    b("launch_pending"),
	}, true
}

// Exited: the pane stays but carries no agent and none is launching: the agent process exited.
func (a HerdrAgent) Exited() bool { return a.Agent == "" && !a.LaunchPending }

// Ready for prompts. Herdr 0.7.5 reports the agent (detected, idle) a few seconds before it
// accepts prompts; until then launch_pending stays set and prompts fail agent_not_ready.
func (a HerdrAgent) Ready() bool {
	return !a.LaunchPending && (a.InteractiveReady || a.Status != agent.PaneUnknown)
}

// Info is the agent as the session host lists it; false without a name.
func (a HerdrAgent) Info() (agent.AgentInfo, bool) {
	if a.Name == "" {
		return agent.AgentInfo{}, false
	}
	return agent.AgentInfo{Name: a.Name, Pane: a.PaneID, Cwd: a.Cwd, Status: a.Status}, true
}

// HostErrorFrom maps Herdr's error codes to agent.HostError.
func HostErrorFrom(target string, err error) error {
	var e RPCError
	if !errors.As(err, &e) {
		return agent.HostError{Kind: agent.HostOther, Detail: err.Error()}
	}
	switch e.Code {
	case "agent_not_found", "pane_not_found":
		return agent.ErrNoAgent(target)
	case "socket_unavailable":
		return agent.HostError{Kind: agent.HostOther, Detail: "herdr server not running"}
	case "client_timeout":
		return agent.HostError{Kind: agent.HostTimeout, Detail: e.Message}
	}
	return agent.HostError{Kind: agent.HostOther, Detail: e.Code + ": " + e.Message}
}

func otherWithPrefix(err error, prefix string) bool {
	var he agent.HostError
	return errors.As(err, &he) && he.Kind == agent.HostOther && strings.HasPrefix(he.Detail, prefix)
}

// IsPaneBusy: agent.start on a pane whose shell is not at its prompt yet (a tab just created).
func IsPaneBusy(err error) bool { return otherWithPrefix(err, "agent_pane_busy:") }

// IsAgentNotReady: the agent exists but is still launching (launch_pending).
func IsAgentNotReady(err error) bool { return otherWithPrefix(err, "agent_not_ready:") }

// IsWorkspaceNotFound: Herdr no longer has the workspace (its server restarted).
func IsWorkspaceNotFound(err error) bool { return otherWithPrefix(err, "workspace_not_found:") }

func shapeErr(method string) error {
	return agent.HostError{Kind: agent.HostOther, Detail: method + ": unexpected result shape"}
}

// Client is the typed Herdr methods over a Transport.
type Client struct {
	t Transport
}

func NewClient(t Transport) *Client { return &Client{t: t} }

func (c *Client) call(ctx context.Context, method, target string, params map[string]any) (map[string]any, error) {
	raw, err := c.t.Request(ctx, method, params)
	if err != nil {
		return nil, HostErrorFrom(target, err)
	}
	var v map[string]any
	_ = json.Unmarshal(raw, &v) // a non-object result is a shape error at the caller
	return v, nil
}

func pointer(v map[string]any, keys ...string) (any, bool) {
	var cur any = v
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[k]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func pointerStr(v map[string]any, keys ...string) (string, bool) {
	x, ok := pointer(v, keys...)
	if !ok {
		return "", false
	}
	s, ok := x.(string)
	return s, ok
}

// Workspace is one workspace's id and label.
type Workspace struct {
	ID    string
	Label string
}

// Workspaces lists every workspace.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	r, err := c.call(ctx, "workspace.list", "", map[string]any{})
	if err != nil {
		return nil, err
	}
	list, ok := r["workspaces"].([]any)
	if !ok {
		return nil, shapeErr("workspace.list")
	}
	var out []Workspace
	for _, w := range list {
		m, _ := w.(map[string]any)
		id, ok1 := m["workspace_id"].(string)
		label, ok2 := m["label"].(string)
		if ok1 && ok2 {
			out = append(out, Workspace{ID: id, Label: label})
		}
	}
	return out, nil
}

// CreateWorkspace makes a new workspace (a fresh server has none, and tab.create needs one);
// returns its id.
func (c *Client) CreateWorkspace(ctx context.Context, label, cwd string) (string, error) {
	r, err := c.call(ctx, "workspace.create", "", map[string]any{"label": label, "cwd": cwd, "focus": false})
	if err != nil {
		return "", err
	}
	id, ok := pointerStr(r, "workspace", "workspace_id")
	if !ok {
		return "", shapeErr("workspace.create")
	}
	return id, nil
}

// CreateTab makes a new tab with env set for its shell; returns its root pane id.
func (c *Client) CreateTab(ctx context.Context, workspaceID, label, cwd string, env []agent.EnvVar) (string, error) {
	m := map[string]any{}
	for _, e := range env {
		m[e.Key] = e.Value
	}
	params := map[string]any{"workspace_id": workspaceID, "label": label, "cwd": cwd, "env": m, "focus": false}
	r, err := c.call(ctx, "tab.create", label, params)
	if err != nil {
		return "", err
	}
	id, ok := pointerStr(r, "root_pane", "pane_id")
	if !ok {
		return "", shapeErr("tab.create")
	}
	return id, nil
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func (c *Client) agentResult(ctx context.Context, method, target string, params map[string]any) (HerdrAgent, error) {
	r, err := c.call(ctx, method, target, params)
	if err != nil {
		return HerdrAgent{}, err
	}
	m, _ := r["agent"].(map[string]any)
	a, ok := parseAgentMap(m)
	if !ok {
		return HerdrAgent{}, shapeErr(method)
	}
	return a, nil
}

// StartAgent types `<kind> <args…>` into the pane's shell. Returns at once (launch_pending).
func (c *Client) StartAgent(ctx context.Context, name, kind, paneID string, args []string, timeoutMs uint64) (HerdrAgent, error) {
	params := map[string]any{"name": name, "kind": kind, "pane_id": paneID, "args": nonNil(args), "timeout_ms": timeoutMs}
	return c.agentResult(ctx, "agent.start", name, params)
}

func (c *Client) GetAgent(ctx context.Context, target string) (HerdrAgent, error) {
	return c.agentResult(ctx, "agent.get", target, map[string]any{"target": target})
}

func (c *Client) Agents(ctx context.Context) ([]HerdrAgent, error) {
	r, err := c.call(ctx, "agent.list", "", map[string]any{})
	if err != nil {
		return nil, err
	}
	list, ok := r["agents"].([]any)
	if !ok {
		return nil, shapeErr("agent.list")
	}
	var out []HerdrAgent
	for _, x := range list {
		m, _ := x.(map[string]any)
		if a, ok := parseAgentMap(m); ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func (c *Client) Prompt(ctx context.Context, target, text string) error {
	_, err := c.call(ctx, "agent.prompt", target, map[string]any{"target": target, "text": text})
	return err
}

func (c *Client) SendKeys(ctx context.Context, target string, keys []string) error {
	_, err := c.call(ctx, "agent.send_keys", target, map[string]any{"target": target, "keys": nonNil(keys)})
	return err
}

// Read is the visible screen (source "visible"; "recent" came back empty in 0.7.5), its last
// lines lines.
func (c *Client) Read(ctx context.Context, target string, lines uint16) (string, error) {
	r, err := c.call(ctx, "agent.read", target, map[string]any{"target": target, "source": "visible", "strip_ansi": true})
	if err != nil {
		return "", err
	}
	text, ok := pointerStr(r, "read", "text")
	if !ok {
		return "", shapeErr("agent.read")
	}
	all := rustLines(text)
	from := max(len(all)-int(lines), 0)
	return strings.Join(all[from:], "\n"), nil
}

func (c *Client) ClosePane(ctx context.Context, paneID string) error {
	_, err := c.call(ctx, "pane.close", paneID, map[string]any{"pane_id": paneID})
	return err
}

// rustLines is Rust's str::lines: split on \n, drop one trailing \r per line, no final empty
// line.
func rustLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, p := range parts {
		parts[i] = strings.TrimSuffix(p, "\r")
	}
	return parts
}
