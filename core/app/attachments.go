package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/attach"
	"github.com/sihekuang/deskpixie/core/voice"
)

// Attachments for the next request (#240): attachments.add, .remove and .list; the set comes
// back as `attachments` events. Pixie's take_clipboard tool asks the shell to read the
// clipboard with a `clipboard.read` event, and the shell answers with clipboard.done.

// clipboardWait is the longest take_clipboard waits for the shell (tests shorten it).
var clipboardWait = 10 * time.Second

const (
	clipNothing = "There's no image or file on the clipboard."
	clipFailed  = "Couldn't read the clipboard."
)

// clipboardReads are take_clipboard's requests waiting for clipboard.done.
type clipboardReads struct {
	mu   sync.Mutex
	next atomic.Uint64
	wait map[string]chan string
}

func (c *clipboardReads) open() (string, chan string) {
	id := "clip-" + strconv.FormatUint(c.next.Add(1), 10)
	ch := make(chan string, 1)
	c.mu.Lock()
	if c.wait == nil {
		c.wait = map[string]chan string{}
	}
	c.wait[id] = ch
	c.mu.Unlock()
	return id, ch
}

func (c *clipboardReads) close(id string) {
	c.mu.Lock()
	delete(c.wait, id)
	c.mu.Unlock()
}

// done answers a request (its error, "" when the read went through); unknown ids are ignored.
func (c *clipboardReads) done(id, errText string) {
	c.mu.Lock()
	ch := c.wait[id]
	delete(c.wait, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- errText
	}
}

// attachments is the agent's store, nil with the agent off.
func (a *App) attachments() *attach.Store {
	if a.agent == nil {
		return nil
	}
	return a.agent.Attachments()
}

// watchAttachments sends the set to the shell on every change.
func (a *App) watchAttachments() {
	if s := a.attachments(); s != nil {
		s.OnChange(func(items []attach.Item) { a.emit("attachments", map[string]any{"items": orEmpty(items)}) })
	}
}

func orEmpty(items []attach.Item) []attach.Item {
	if items == nil {
		return []attach.Item{}
	}
	return items
}

func (a *App) registerAttachments(r Registrar) {
	r.Register("attachments.add", func(args json.RawMessage) (any, error) {
		var in struct {
			Name string `json:"name"`
			Data string `json:"data"`
			Path string `json:"path"`
		}
		if err := decodeArgs("attachments.add", args, &in); err != nil {
			return nil, err
		}
		s := a.attachments()
		if s == nil {
			return nil, errNoCoordinator
		}
		var it attach.Item
		var err error
		if in.Path != "" {
			it, err = s.AddFile(in.Path)
		} else {
			data, derr := base64.StdEncoding.DecodeString(in.Data)
			if derr != nil {
				return nil, errors.New("attachments.add: data is not base64")
			}
			it, err = s.Add(in.Name, data)
		}
		if err != nil {
			return nil, err
		}
		return map[string]any{"item": it}, nil
	})
	r.Register("attachments.remove", func(args json.RawMessage) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := decodeArgs("attachments.remove", args, &in); err != nil {
			return nil, err
		}
		if s := a.attachments(); s != nil {
			s.Remove(in.ID)
		}
		return struct{}{}, nil
	})
	r.Register("attachments.list", func(json.RawMessage) (any, error) {
		var items []attach.Item
		if s := a.attachments(); s != nil {
			items = s.Pending()
		}
		return map[string]any{"items": orEmpty(items)}, nil
	})
	r.Register("clipboard.done", func(args json.RawMessage) (any, error) {
		var in struct {
			Request string `json:"request"`
			Error   string `json:"error"`
		}
		if err := decodeArgs("clipboard.done", args, &in); err != nil {
			return nil, err
		}
		a.clip.done(in.Request, in.Error)
		return struct{}{}, nil
	})
}

// clipboardTool is Pixie's take_clipboard.
func (a *App) clipboardTool() voice.Tool { return takeClipboard{a} }

type takeClipboard struct{ a *App }

func (takeClipboard) Name() string { return "take_clipboard" }

func (takeClipboard) Description() string {
	return "Attach what the user has copied (a screenshot, an image or files) to their next request to " +
		"the coordinator. Only call this when the user explicitly asks you to take, look at or use " +
		"their screenshot or what they copied in this request. Never call it on your own because " +
		"something might be on the clipboard, or because a request mentions the screen. Then call " +
		"delegate with their request."
}

func (takeClipboard) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}

// WaitedFor: the delegate Pixie calls with take_clipboard waits for what it attaches (#367).
func (takeClipboard) WaitedFor() {}

func (t takeClipboard) Invoke(ctx context.Context, _ json.RawMessage) voice.ToolOutcome {
	s := t.a.attachments()
	if s == nil {
		return clipReply(map[string]any{"error": errNoCoordinator.Error()})
	}
	before := map[string]bool{}
	for _, it := range s.Pending() {
		before[it.ID] = true
	}
	return t.read(ctx, s, before)
}

// read asks the shell for the clipboard and says what it attached that wasn't in before.
func (t takeClipboard) read(ctx context.Context, s *attach.Store, before map[string]bool) voice.ToolOutcome {
	id, ch := t.a.clip.open()
	defer t.a.clip.close(id)
	t.a.emit("clipboard.read", map[string]any{"request": id})
	var errText string
	select {
	case errText = <-ch:
	case <-time.After(clipboardWait):
		errText = clipFailed
	case <-ctx.Done():
		errText = clipFailed
	}
	if errText != "" {
		return clipReply(map[string]any{"error": errText})
	}
	var names []string
	for _, it := range s.Pending() {
		if !before[it.ID] {
			names = append(names, it.Name)
		}
	}
	if len(names) == 0 {
		return clipReply(map[string]any{"error": clipNothing})
	}
	return clipReply(map[string]any{"attached": names})
}

func clipReply(v any) voice.ToolOutcome {
	b, _ := json.Marshal(v)
	return voice.ToolOutcome{Output: b}
}
