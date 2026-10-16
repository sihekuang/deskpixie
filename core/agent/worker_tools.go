package agent

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The pixie MCP tools (port of worker_tools.rs): the coordinator's worker tools and the
// workers' report tools, as data. The transport (JSON-RPC over HTTP) lives in the HTTP adapter;
// this file says which identity may call what, checks and cleans the arguments, and words the
// lines the coordinator's inbox gets. All agent text is data: cleaned, capped, never interpreted.

const (
	// MaxProgressWords: report_progress summaries keep at most this many words.
	MaxProgressWords = 20
	// MaxReportWords: result summaries and evidence, questions and block reasons keep at most
	// this many words each.
	MaxReportWords = 35
	// MaxOptions: ask_coordinator keeps at most this many options, each at most MaxOptionWords words.
	MaxOptions     = 3
	MaxOptionWords = 12
	// MaxSilentTurnChars: a worker turn that ended without a report forwards at most this many characters.
	MaxSilentTurnChars = 1_200
	// MaxTaskChars: start_worker tasks and message_worker texts keep at most this many characters.
	MaxTaskChars = 4_000

	maxRepoChars  = 256
	maxNameChars  = 64
	maxQuoteChars = 400
)

// Role is who is calling, from the bearer's identity.
type Role int

const (
	RoleCoordinator Role = iota
	RoleWorker
)

func RoleOf(identity string) Role {
	if identity == Coordinator {
		return RoleCoordinator
	}
	return RoleWorker
}

var (
	CoordinatorTools = [5]string{"start_worker", "create_project", "message_worker", "stop_worker", "list_workers"}
	WorkerTools      = [4]string{"report_progress", "report_result", "ask_coordinator", "report_blocked"}
)

// ToolParseError: UnknownTool (a JSON-RPC "unknown tool" error: not a tool this identity may
// call), else Msg is a tool error the agent sees (`isError: true`; Rust's BadArgs).
type ToolParseError struct {
	UnknownTool bool
	Msg         string
}

func (e ToolParseError) Error() string {
	if e.UnknownTool {
		return "unknown tool"
	}
	return e.Msg
}

func badArgs(msg string) error { return ToolParseError{Msg: msg} }

func stringProps(props ...[2]string) map[string]any {
	p := map[string]any{}
	for _, kd := range props {
		p[kd[0]] = map[string]any{"type": "string", "description": kd[1]}
	}
	return p
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// Catalog is the MCP `tools/list` entries for role.
func Catalog(role Role) []map[string]any {
	if role == RoleCoordinator {
		return []map[string]any{
			{
				"name":        "start_worker",
				"description": "Start a worker agent on a repository: it gets its own git worktree under <repo>/.worktrees/ on a `pixie/…` branch, works there, and reports back to you as a message. Returns {name, worktree} or an error.",
				"inputSchema": objectSchema(map[string]any{
					"kind":   map[string]any{"type": "string", "enum": []string{"claude", "codex"}, "description": "Which agent: claude or codex."},
					"repo":   map[string]any{"type": "string", "description": "The repository: its folder name under the projects root (e.g. focus), or its path."},
					"task":   map[string]any{"type": "string", "description": "What the worker must do, completely: it sees nothing else."},
					"job_id": map[string]any{"type": "string", "description": "The job this work is for: the [ref job_…] of the request."},
					"name":   map[string]any{"type": "string", "description": "Required: a 2–3 word name for this piece of work, e.g. `fix-login`. Reusing a name reuses that worker's folder and branch."},
					"override_existing_session": map[string]any{"type": "boolean", "description": "True only when the person explicitly asked for a worker, or to run this separately or in the background, even though a pix session already matches this repo. Set after a refusal names that session; never as a default."},
				}, "kind", "repo", "task", "job_id"),
			},
			{
				"name":        "create_project",
				"description": "Create a new, empty project under the projects root: a folder with a git repository and one empty commit, ready for start_worker. Use it instead of mkdir or git init. Refuses a name that already exists. Returns {name, path} or an error.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The new project's folder name: letters, digits, '-', '_' or '.', e.g. spacex-dashboard."}), "name"),
			},
			{
				"name":        "message_worker",
				"description": "Send a message to a running worker (for example the answer to its question). It is delivered when the worker is idle.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The worker's name."}, [2]string{"text", "The message."}), "name", "text"),
			},
			{
				"name":        "stop_worker",
				"description": "Stop a worker: it is interrupted and its terminal closes. Its worktree and branch are kept. Call it as soon as a worker's done report checks out and you have nothing more for it.",
				"inputSchema": objectSchema(stringProps([2]string{"name", "The worker's name."}), "name"),
			},
			{
				"name":        "list_workers",
				"description": "The workers of open jobs and any still running: name, kind, repo, job_id, status.",
				"inputSchema": objectSchema(map[string]any{}),
			},
		}
	}
	return []map[string]any{
		{
			"name":        "report_progress",
			"description": "Tell the coordinator what you are doing, in at most 20 words. Use it now and then on long tasks.",
			"inputSchema": objectSchema(stringProps([2]string{"summary", "What you are doing, at most 20 words."}), "summary"),
		},
		{
			"name":        "report_result",
			"description": "Report the outcome of your task when you finish: done or failed, a summary and the evidence (tests run, the commit), each at most 35 words. The summary is spoken aloud to the user at once as your own report: write it as one or two plain spoken sentences, with no paths, code or links.",
			"inputSchema": objectSchema(map[string]any{
				"outcome":  map[string]any{"type": "string", "enum": []string{"done", "failed"}},
				"summary":  map[string]any{"type": "string", "description": "What you did or why it failed, at most 35 words, as plain speech: it is read aloud to the user."},
				"evidence": map[string]any{"type": "string", "description": "Proof: tests run and their result, the commit, at most 35 words."},
			}, "outcome", "summary", "evidence"),
		},
		{
			"name":        "ask_coordinator",
			"description": "Ask the coordinator a question you need answered to continue (at most 35 words, up to 3 options), then end your turn. The answer arrives as a message.",
			"inputSchema": objectSchema(map[string]any{
				"question": map[string]any{"type": "string", "description": "The question, at most 35 words."},
				"options":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 3, "description": "Up to 3 short options."},
			}, "question"),
		},
		{
			"name":        "report_blocked",
			"description": "Report that you cannot continue and why, at most 35 words. The reason is spoken aloud to the user: write it as plain speech, with no paths, code or links.",
			"inputSchema": objectSchema(stringProps([2]string{"reason", "Why you are blocked, at most 35 words, as plain speech."}), "reason"),
		},
	}
}

// ParseToolCall checks a `tools/call` from role and turns it into an McpCall. A tool of the
// other role is UnknownTool (per-identity tools: a worker can't start workers, the coordinator
// can't report). args is the decoded JSON arguments (normally map[string]any).
func ParseToolCall(role Role, name string, args any) (McpCall, error) {
	allowed := slices.Contains(CoordinatorTools[:], name) || IsSessionTool(name)
	if role == RoleWorker {
		allowed = slices.Contains(WorkerTools[:], name)
	}
	if !allowed {
		return McpCall{}, ToolParseError{UnknownTool: true}
	}
	obj, _ := args.(map[string]any)
	text := func(k string) (string, bool) {
		s, ok := obj[k].(string)
		s = strings.TrimSpace(s)
		return s, ok && s != ""
	}
	need := func(k string) (string, error) {
		if s, ok := text(k); ok {
			return s, nil
		}
		return "", badArgs(k + " is required")
	}
	nonEmpty := func(k, v string) (string, error) {
		if v == "" {
			return "", badArgs(k + " is required")
		}
		return v, nil
	}
	needName := func(k string) (string, error) {
		v, err := need(k)
		if err != nil {
			return "", err
		}
		return nonEmpty(k, singleLine(v))
	}
	needQuote := func(k string, words int) (string, error) {
		v, err := need(k)
		if err != nil {
			return "", err
		}
		return nonEmpty(k, Quote(v, words))
	}
	needPlain := func(k string, max int) (string, error) {
		v, err := need(k)
		if err != nil {
			return "", err
		}
		return nonEmpty(k, Plain(v, max))
	}

	switch name {
	case "start_worker":
		k, err := need("kind")
		if err != nil {
			return McpCall{}, err
		}
		kind, ok := ParseAgentKind(k)
		if !ok {
			return McpCall{}, badArgs("kind must be claude or codex")
		}
		repo, err := need("repo")
		if err != nil {
			return McpCall{}, err
		}
		if utf8.RuneCountInString(repo) > maxRepoChars || strings.IndexFunc(repo, isControl) >= 0 {
			return McpCall{}, badArgs("repo is not a folder name or path")
		}
		jobID, err := need("job_id")
		if err != nil {
			return McpCall{}, err
		}
		if _, ok := ParseJobID(jobID); !ok {
			return McpCall{}, badArgs("job_id must look like job_0123456789ab")
		}
		task, err := needPlain("task", MaxTaskChars)
		if err != nil {
			return McpCall{}, err
		}
		hint := ""
		if n, ok := text("name"); ok {
			hint = singleLine(n)
		}
		override, _ := obj["override_existing_session"].(bool)
		return McpCall{Kind: CallStartWorker, Worker: WorkerRequest{Kind: kind, Repo: repo, Task: task, JobID: jobID, Name: hint, OverrideExistingSession: override}}, nil
	case "create_project":
		n, err := need("name")
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallCreateProject, Name: n}, nil
	case "message_worker":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		t, err := needPlain("text", MaxTaskChars)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallMessageWorker, Name: n, Text: t}, nil
	case "stop_worker":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallStopWorker, Name: n}, nil
	case "list_workers":
		return McpCall{Kind: CallListWorkers}, nil
	case "report_progress":
		s, err := needQuote("summary", MaxProgressWords)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallReportProgress, Summary: s}, nil
	case "report_result":
		o, err := need("outcome")
		if err != nil {
			return McpCall{}, err
		}
		var outcome Outcome
		switch o {
		case "done":
			outcome = OutcomeDone
		case "failed":
			outcome = OutcomeFailed
		default:
			return McpCall{}, badArgs("outcome must be done or failed")
		}
		s, err := needQuote("summary", MaxReportWords)
		if err != nil {
			return McpCall{}, err
		}
		evidence, ok := text("evidence")
		if !ok {
			evidence = "none given"
		}
		return McpCall{Kind: CallReportResult, Outcome: outcome, Summary: s, Evidence: Quote(evidence, MaxReportWords)}, nil
	case "ask_coordinator":
		var options []string
		switch v := obj["options"].(type) {
		case nil:
		case []any:
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					continue
				}
				if q := Quote(s, MaxOptionWords); q != "" && len(options) < MaxOptions {
					options = append(options, q)
				}
			}
		default:
			return McpCall{}, badArgs("options must be a list of strings")
		}
		q, err := needQuote("question", MaxReportWords)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallAskCoordinator, Question: q, Options: options}, nil
	case "list_sessions":
		return McpCall{Kind: CallListSessions}, nil
	case "message_session":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		t, err := needPlain("text", MaxTaskChars)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallMessageSession, Name: n, Text: t}, nil
	case "interrupt_session", "close_session":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		if name == "close_session" {
			return McpCall{Kind: CallCloseSession, Name: n}, nil
		}
		return McpCall{Kind: CallInterruptSession, Name: n}, nil
	case "describe_session":
		n, err := needName("name")
		if err != nil {
			return McpCall{}, err
		}
		t, _ := text("text")
		return McpCall{Kind: CallDescribeSession, Name: n, Text: singleLine(t)}, nil
	case "report_blocked":
		r, err := needQuote("reason", MaxReportWords)
		if err != nil {
			return McpCall{}, err
		}
		return McpCall{Kind: CallReportBlocked, Reason: r}, nil
	}
	return McpCall{}, ToolParseError{UnknownTool: true}
}

// defang is cleaned agent text with nothing left that could open a tag or close its own
// quote: no markup, control characters, fences, double quotes, or stray `<` / `>` (an
// unterminated `<spoken` tag survives Clean).
func defang(text string) string {
	return strings.NewReplacer(`"`, "'", "“", "'", "”", "'", "<", " ", ">", " ").Replace(Clean(text))
}

// Quote is agent text for a quoted inbox line: defanged, at most maxWords words and 400 characters.
func Quote(text string, maxWords int) string {
	words := strings.Fields(defang(text))
	var out string
	if len(words) > maxWords {
		out = strings.Join(words[:maxWords], " ") + "…"
	} else {
		out = strings.Join(words, " ")
	}
	if utf8.RuneCountInString(out) > maxQuoteChars {
		out = strings.TrimRightFunc(string([]rune(out)[:maxQuoteChars]), unicode.IsSpace) + "…"
	}
	return out
}

// singleLine is a one-line name hint: Plain with newlines and tabs turned into spaces.
func singleLine(text string) string {
	return Plain(strings.NewReplacer("\n", " ", "\t", " ").Replace(text), maxNameChars)
}

// Plain is free text typed into an agent (a task, a message): control characters other than
// newlines and tabs removed, trimmed, at most maxChars characters.
func Plain(text string, maxChars int) string {
	var b strings.Builder
	for _, c := range text {
		if (c == '\n' || c == '\t' || !isControl(c)) && !isFormatChar(c) {
			b.WriteRune(c)
		}
	}
	kept := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(kept) > maxChars {
		return string([]rune(kept)[:maxChars]) + "…"
	}
	return kept
}

// ResultLine is `Worker <name> reports done: "<summary>" Evidence: "<evidence>"`.
func ResultLine(name string, outcome Outcome, summary, evidence string) string {
	return fmt.Sprintf("Worker %s reports %s: \"%s\" Evidence: \"%s\"", name, outcome, summary, evidence)
}

// WorkerWho names a worker for the user: "The fix login worker in focus".
func WorkerWho(plan WorkerPlan) string {
	work := strings.ReplaceAll(plan.Slug, "-", " ")
	if work == "" {
		return "A worker in " + plan.RepoName
	}
	return fmt.Sprintf("The %s worker in %s", work, plan.RepoName)
}

// WorkerResultSpoken is a worker's report_result as Pixie says it to the user at once, clearly
// the worker's own word, not a checked result: "<who> says it's done: <summary>." /
// "<who> couldn't finish: <summary>."
func WorkerResultSpoken(who string, outcome Outcome, summary string) string {
	if outcome == OutcomeFailed {
		return workerSpoken(who+" couldn't finish", summary)
	}
	return workerSpoken(who+" says it's done", summary)
}

// WorkerBlockedSpoken is a worker's report_blocked as Pixie says it: "<who> is stuck: <reason>."
func WorkerBlockedSpoken(who, reason string) string {
	return workerSpoken(who+" is stuck", reason)
}

// workerSpoken is `<head>: <text>.` made speakable: quoted (defanged, capped), links dropped,
// paths cut to their file name, then capped like a <spoken> block (2 sentences, 35 words).
func workerSpoken(head, text string) string {
	var words []string
	for _, w := range strings.Fields(Quote(text, MaxReportWords)) {
		lower := strings.ToLower(w)
		switch {
		case strings.Contains(lower, "://") || strings.HasPrefix(lower, "www."):
			continue
		case strings.ContainsAny(w, "/\\"):
			trail := strings.TrimLeft(strings.TrimRight(w, ".,;:!?)"), "(")
			name, ok := pathFileName(strings.ReplaceAll(trail, "\\", "/"))
			if !ok {
				continue
			}
			w = name + w[len(strings.TrimRight(w, ".,;:!?)")):]
		}
		words = append(words, w)
	}
	body := strings.Join(words, " ")
	if body == "" {
		return Cap(head + ".")
	}
	if last, _ := utf8.DecodeLastRuneInString(body); !strings.ContainsRune(".!?…", last) {
		body += "."
	}
	return Cap(head + ": " + body)
}

// WorkerQuestionLine is `Worker <name> asks: "<question>" Options: "a" | "b". Answer with message_worker.`
func WorkerQuestionLine(name, question string, options []string) string {
	line := fmt.Sprintf("Worker %s asks: \"%s\"", name, question)
	if len(options) > 0 {
		opts := make([]string, len(options))
		for k, o := range options {
			opts[k] = "\"" + o + "\""
		}
		line += " Options: " + strings.Join(opts, " | ")
	}
	return line + " Answer with message_worker."
}

// BlockedLine is `Worker <name> reports blocked: "<reason>"`.
func BlockedLine(name, reason string) string {
	return fmt.Sprintf("Worker %s reports blocked: \"%s\"", name, reason)
}

// SilentTurnLine is a worker turn that ended without any report: its last words (cleaned,
// capped at 1,200 chars).
func SilentTurnLine(name, lastWords string) string {
	text := defang(lastWords)
	if utf8.RuneCountInString(text) > MaxSilentTurnChars {
		text = string([]rune(text)[:MaxSilentTurnChars]) + "…"
	}
	if text == "" {
		return fmt.Sprintf("Worker %s ended its turn without a report and said nothing.", name)
	}
	return fmt.Sprintf("Worker %s ended its turn without a report. Its last words: \"%s\"", name, text)
}

// ExitedLine: the worker's pane went away without anyone stopping it.
func ExitedLine(name string) string {
	return fmt.Sprintf("Worker %s stopped unexpectedly (its terminal closed).", name)
}

// NeverStartedLine: a worker never showed any sign of its prompt, even after it was re-sent; it was closed.
func NeverStartedLine(name string) string {
	return fmt.Sprintf("Worker %s failed: The worker never started on its task.", name)
}

// UnreachableLine: a worker's messages could not be typed into its pane, several times.
func UnreachableLine(name string) string {
	return fmt.Sprintf("Worker %s could not be reached; your message was not delivered.", name)
}
