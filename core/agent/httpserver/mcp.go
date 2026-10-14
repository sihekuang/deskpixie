package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
)

// `POST /mcp`: the pixie MCP server (Rust: crates/agent-http/src/mcp.rs), stateless
// Streamable HTTP.
//
// The subset served: JSON-RPC 2.0 single messages (no batches) with `initialize`, `ping`,
// `tools/list` and `tools/call`; notifications (`notifications/initialized`, …) and client
// responses get 202 Accepted. Every answer is one `application/json` body (no SSE stream, no
// `Mcp-Session-Id`); GET and DELETE are 405. The bearer is checked before the body is read
// (401), bodies are capped at 16 KB (413), and a request with an Origin header (a browser
// page) is refused (403). Which tools an agent sees and may call depends on its identity.

// Constants copied verbatim from ai-mascot crates/agent-http/src/mcp.rs.
const (
	// MaxMcpBody: MCP request bodies above this are refused (413). (mcp.rs MAX_MCP_BODY)
	MaxMcpBody = 16 * 1024
	// McpCallLimit is the longest a tool call waits for the app (a start_worker makes a
	// worktree and starts a pane). (mcp.rs MCP_CALL_LIMIT; Rust asserts it equals
	// agent_core::runtime::MCP_CALL_LIMIT — P9 adds that relation test.)
	McpCallLimit = 150 * time.Second
	// ServerName is serverInfo.name (spec §6 rename; Rust: "ai-mascot").
	ServerName = "deskpixie"
	// ServerVersion is serverInfo.version (Rust: agent-http's CARGO_PKG_VERSION).
	ServerVersion = "0.1.0"
)

// ProtocolVersions this server speaks; a client's own is echoed when listed, else the second.
// (mcp.rs PROTOCOL_VERSIONS)
var ProtocolVersions = [4]string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

func rpcError(id any, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// toolResult is a tool result the agent reads as text (isError for failures it should see and
// handle).
func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isError}
}

type rpcFailure struct {
	code    int
	message string
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	if hasOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	agentName, ok := s.bearer(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, tooLarge, err := readBody(w, r, MaxMcpBody)
	if err != nil {
		if tooLarge {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	msg, err := decodeJSON(body)
	if err != nil {
		json200(w, rpcError(nil, codeParseError, "Parse error"))
		return
	}
	obj, ok := msg.(map[string]any)
	if !ok {
		json200(w, rpcError(nil, codeInvalidRequest, "Invalid Request (batches are not supported)"))
		return
	}
	id := obj["id"]
	method, ok := obj["method"].(string)
	if !ok {
		// A response to a server request (we send none) or junk.
		_, isResult := obj["result"]
		_, isError := obj["error"]
		if isResult || isError {
			w.WriteHeader(http.StatusAccepted)
		} else {
			json200(w, rpcError(id, codeInvalidRequest, "Invalid Request"))
		}
		return
	}
	if id == nil {
		// A notification (`notifications/initialized`, `notifications/cancelled`, …).
		w.WriteHeader(http.StatusAccepted)
		return
	}
	params, ok := obj["params"]
	if !ok {
		params = map[string]any{}
	}
	role := agent.RoleOf(agentName)
	var result any
	var fail *rpcFailure
	switch method {
	case "initialize":
		result = initialize(params)
	case "ping":
		result = map[string]any{}
	case "tools/list":
		result = map[string]any{"tools": agent.Catalog(role)}
	case "tools/call":
		result, fail = s.call(r, agentName, role, params)
	default:
		fail = &rpcFailure{codeMethodNotFound, "Method not found"}
	}
	if fail != nil {
		json200(w, rpcError(id, fail.code, fail.message))
		return
	}
	json200(w, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func initialize(params any) map[string]any {
	p, _ := params.(map[string]any)
	asked, _ := p["protocolVersion"].(string)
	version := ProtocolVersions[1]
	for _, v := range ProtocolVersions {
		if v == asked {
			version = v
		}
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": ServerName, "version": ServerVersion},
	}
}

func (s *Server) call(r *http.Request, agentName string, role agent.Role, params any) (any, *rpcFailure) {
	p, _ := params.(map[string]any)
	name, ok := p["name"].(string)
	if !ok {
		return nil, &rpcFailure{codeInvalidParams, "Missing tool name"}
	}
	args, ok := p["arguments"]
	if !ok {
		args = map[string]any{}
	}
	call, err := agent.ParseToolCall(role, name, args)
	if err != nil {
		var pe agent.ToolParseError
		if errors.As(err, &pe) && pe.UnknownTool {
			return nil, &rpcFailure{codeInvalidParams, "Unknown tool: " + agent.Quote(name, 4)}
		}
		return toolResult(err.Error(), true), nil
	}
	reply := make(chan agent.McpReply, 1)
	if !s.rt.Deliver(agent.HookEvent{Kind: agent.HookMcp, Agent: agentName, Call: call, McpReply: reply}) {
		return toolResult("The app is shutting down.", true), nil
	}
	timer := time.NewTimer(s.mcpCallLimit)
	defer timer.Stop()
	select {
	case rep, ok := <-reply:
		if !ok {
			break
		}
		if rep.Err != nil {
			return toolResult(rep.Err.Error(), true), nil
		}
		text, err := encodeJSON(rep.Value)
		if err != nil {
			return toolResult("The app gave an answer that is not JSON.", true), nil
		}
		return toolResult(text, false), nil
	case <-timer.C:
	case <-r.Context().Done():
	}
	return toolResult("The app did not answer.", true), nil
}
