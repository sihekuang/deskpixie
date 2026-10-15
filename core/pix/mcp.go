package pix

import (
	"bufio"
	"encoding/json"
	"io"
	"time"

	"github.com/sihekuang/deskpixie/core/pix/wire"
)

var setDescriptionTool = map[string]any{
	"name":        "set_description",
	"description": "Say in a few words what you are working on, so the user and Pixie can tell this session apart. Call it when your task changes.",
	"inputSchema": map[string]any{
		"type":       "object",
		"properties": map[string]any{"text": map[string]any{"type": "string", "description": "A few words, e.g. \"fixing login redirect loop\"."}},
		"required":   []string{"text"},
	},
}

// RunMCP is `pix mcp`: a stdio MCP server (newline-delimited JSON-RPC) with set_description,
// which it hands to the parent pix. It is a child of the agent, so it is there even while
// DeskPixie isn't.
func RunMCP(sock string, in io.Reader, out io.Writer) error {
	s := bufio.NewScanner(in)
	s.Buffer(make([]byte, 64<<10), wire.MaxLine)
	enc := json.NewEncoder(out)
	for s.Scan() {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(s.Bytes(), &req) != nil || req.ID == nil {
			continue // notifications and junk get no reply
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "pixie", "version": "1"}}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": []any{setDescriptionTool}}
		case "tools/call":
			var p struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			text, isErr := "ok", false
			if p.Name != "set_description" {
				text, isErr = "unknown tool "+p.Name, true
			} else if reply, err := askLocal(sock, wire.Msg{Kind: wire.KindEvent, Event: "description", Source: "agent", Description: p.Arguments["text"]}, 4*time.Second); err != nil {
				text, isErr = "pix is not reachable; the description was not saved", true
			} else if reply.Err != "" {
				text, isErr = reply.Err, true
			}
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": isErr}
		default:
			enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	return s.Err()
}
