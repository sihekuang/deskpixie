package agentapp

// The delegation side's view of the desktop and the voice loop: the chime and notification,
// the echo gate, the jsonl store and the voice link. Ports of src-tauri/src/agent/{notify.rs,
// echo.rs, store.rs, voice_link.rs}.

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sihekuang/deskpixie/core/agent"
	agentfs "github.com/sihekuang/deskpixie/core/agent/fs"
	"github.com/sihekuang/deskpixie/core/app"
	"github.com/sihekuang/deskpixie/core/voice"
)

// ---------- notify.rs ----------

// Chime is the sound for an agent reply while no conversation is open.
const Chime = "/System/Library/Sounds/Glass.aiff"

// Notifier chimes with afplay (as Rust) and shows notifications through the app's `notify`
// event (Swift's UNUserNotificationCenter; Rust used osascript). The controller caps
// notifications at agent.notifyPerMinute (questions bypass the cap).
type Notifier struct {
	runner agent.CommandRunner
	notify func(title, body string)
	// notifyUpdate: a notification about a bubble line (#199); nil falls back to notify.
	notifyUpdate func(title, body string, update any)
}

func NewNotifier(runner agent.CommandRunner, notify func(title, body string)) *Notifier {
	return &Notifier{runner: runner, notify: notify}
}

// WithUpdates sets the notification about a bubble line (#199).
func (n *Notifier) WithUpdates(notifyUpdate func(title, body string, update any)) *Notifier {
	n.notifyUpdate = notifyUpdate
	return n
}

func (n *Notifier) Chime() {
	argv := []string{"/usr/bin/afplay", Chime}
	if err := n.runner.SpawnDetached(argv, []agent.EnvVar{{Key: "PATH", Value: "/usr/bin:/bin"}}); err != nil {
		log.Printf("agent: %s failed: %v", argv[0], err)
	}
}

func (n *Notifier) Notify(title, body string) {
	if n.notify != nil {
		n.notify(title, body)
	}
}

func (n *Notifier) NotifyUpdate(title, body string, update agent.UpdateItem) {
	if n.notifyUpdate == nil {
		n.Notify(title, body)
		return
	}
	n.notifyUpdate(title, body, update)
}

// pixieCue: Happy → Pixie celebrates, Error → Pixie flashes the error state.
type pixieCue struct{ hooks app.AgentHooks }

func (m pixieCue) Cue(cue agent.Cue) {
	switch cue {
	case agent.CueHappy:
		call(m.hooks.Celebrate)
	case agent.CueError:
		call(m.hooks.FlashError)
	}
}

// updatesSink: the updates bubble's list → the app's `updates` event.
type updatesSink struct {
	hooks app.AgentHooks
	feed  *threadFeed // nil: no thread
}

func (u updatesSink) Updates(items []agent.UpdateItem) {
	if u.hooks.Updates != nil {
		u.hooks.Updates(items)
	}
	u.feed.requests(items)
}

// paneSink: an agent waiting at a prompt in its terminal → the app's `showPane` event.
type paneSink struct{ hooks app.AgentHooks }

func (p paneSink) ShowPane(job agent.JobID, worker string) {
	if p.hooks.ShowPane != nil {
		p.hooks.ShowPane(string(job), worker)
	}
}

// focusSink: a FocusPlan to show → the app's `focusPlan` event.
type focusSink struct{ hooks app.AgentHooks }

func (f focusSink) Focus(plan agent.FocusPlan) {
	if f.hooks.Focus != nil {
		f.hooks.Focus(plan)
	}
}

// chatSink: a typed job's line → the app's `chat.line` event (#221), and its answers to the
// thread (#302).
type chatSink struct {
	hooks app.AgentHooks
	feed  *threadFeed // nil: no thread
}

func (c chatSink) Chat(l agent.ChatLine) {
	c.hooks.Chat(l)
	c.feed.answer(l)
}

// ChatEnded: a typed job ended with no line (Stop): its live step goes from the thread.
func (c chatSink) ChatEnded(job agent.JobID) { c.feed.stopped(job) }

// replySink: a reply card → the app's `reply.card` event.
type replySink struct{ hooks app.AgentHooks }

func (r replySink) Reply(c agent.ReplyCard) {
	if r.hooks.Reply != nil {
		r.hooks.Reply(c)
	}
}

type rosterSink struct {
	hooks app.AgentHooks
	feed  *threadFeed // nil: no thread
}

func (r rosterSink) Roster(v agent.RosterView) {
	r.feed.setRoster(v)
	if r.hooks.Roster != nil {
		r.hooks.Roster(v)
	}
}

// lineSink: a reply line → the thread's ThreadLine hook (#302).
type lineSink struct{ feed *threadFeed }

func (l lineSink) Line(line agent.OutboxLine) { l.feed.line(line) }

// Delivered: lines heard or seen elsewhere are read in the thread.
func (l lineSink) Delivered(ids []uint64) { l.feed.delivered(ids) }

func call(f func()) {
	if f != nil {
		f()
	}
}

// ---------- echo.rs ----------

// EchoTail: user words this soon after Pixie stopped speaking are still treated as possible echo.
const EchoTail = voice.EchoTail

// EchoGate (defence in depth): Pixie's own speech, picked up by the mic and transcribed as the
// user's words, must never reach the delegation controller (where it could say a confirm
// word). Driven by the mouth level the voice runtime reports: it is non-zero only while Pixie's
// reply is audibly playing. The zero value is quiet. Not safe for concurrent use on its own.
type EchoGate struct {
	speaking bool
	ended    time.Time // zero: never ended
}

// OnMouth is each mouth level the voice runtime reports.
func (g *EchoGate) OnMouth(level int, now time.Time) {
	if level > 0 {
		g.speaking = true
	} else if g.speaking {
		g.speaking = false
		g.ended = now
	}
}

func (g *EchoGate) blocking(now time.Time) bool {
	return g.speaking || (!g.ended.IsZero() && now.Sub(g.ended) < EchoTail)
}

// Admits is false for UserSaid while Pixie is speaking or within EchoTail after; true otherwise.
func (g *EchoGate) Admits(s voice.VoiceSignal, now time.Time) bool {
	return !(s.Kind == voice.SigUserSaid && g.blocking(now))
}

// ---------- store.rs ----------

// JsonlStore is agent.Store over jsonl files in <app_data>/agent.
type JsonlStore struct {
	dir string
	mu  sync.Mutex
}

func NewJsonlStore(dir string) *JsonlStore { return &JsonlStore{dir: dir} }

func (s *JsonlStore) Load(file string) []string {
	b, err := os.ReadFile(filepath.Join(s.dir, file))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("agent: cannot read %s: %v", file, err)
		}
		return nil
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 64<<20)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

func (s *JsonlStore) Append(file, line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, file)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.WriteString(line + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		log.Printf("agent: cannot append to %s: %v", path, err)
	}
}

// Rewrite replaces the file atomically and 0600 (the live worktrees file names repos; jobs
// hold the user's tasks).
func (s *JsonlStore) Rewrite(file string, lines []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, file)
	text := strings.Join(lines, "\n")
	if text != "" {
		text += "\n"
	}
	if err := agentfs.WritePrivate(path, text); err != nil {
		log.Printf("agent: cannot rewrite %s: %v", path, err)
	}
}

// ---------- voice_link.rs ----------

// VoiceLink is agent.VoiceLink over the voice loop. Built before the voice loop (its tools
// need the agent handle), then attached once the voice loop exists.
type VoiceLink struct {
	voice   atomic.Pointer[app.AgentVoice]
	open    atomic.Bool
	refresh func()
}

func (l *VoiceLink) attach(v app.AgentVoice) { l.voice.CompareAndSwap(nil, &v) }

// setOpen tracks SigOpened / SigClosed (set by the signal forwarder).
func (l *VoiceLink) setOpen(open bool) { l.open.Store(open) }

func (l *VoiceLink) IsOpen() bool { return l.voice.Load() != nil && l.open.Load() }

func (l *VoiceLink) Inject(n agent.Notice) {
	if v := l.voice.Load(); v != nil {
		(*v).Inject(n)
	}
}

// WithdrawHeld drops a notice not said yet that says session's message still waits (#339).
func (l *VoiceLink) WithdrawHeld(session string, upTo uint64) {
	if v := l.voice.Load(); v != nil {
		if w, ok := (*v).(agent.HeldWithdrawer); ok {
			w.WithdrawHeld(session, upTo)
		}
	}
}

func (l *VoiceLink) Open() bool {
	if v := l.voice.Load(); v != nil {
		return (*v).Open()
	}
	return false
}

func (l *VoiceLink) HoldOpen(on bool) {
	if v := l.voice.Load(); v != nil {
		(*v).HoldOpen(on)
	}
	// A job opened or closed: the tray's Agent ▸ job list changed.
	call(l.refresh)
}
