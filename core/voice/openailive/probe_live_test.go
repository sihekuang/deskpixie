//go:build live

package openailive_test

// Client-delegation probe (#70, docs/parity/gpt-live-client-delegation.md). Raw WebSocket, no
// adapter: it opens a GPT-Live session with delegation {type: client}, speaks synthesized
// questions (say + afconvert), streams silence in between (the timeline only advances with
// audio), and logs every server event except audio payloads to $PROBE_OUT (JSON lines). Run with:
//
//	PROBE_GPTLIVE=1 PROBE_OUT=/tmp/probe.jsonl go test -tags live -run TestLiveProbeClientDelegation -v -timeout 10m ./voice/openailive/
//
// About 3 minutes of session time (~$0.15). The key is never printed.

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/sihekuang/deskpixie/core/audio"
	"github.com/sihekuang/deskpixie/core/voice"
)

func probeSpeech(t *testing.T, text string) []int16 {
	t.Helper()
	dir := t.TempDir()
	aiff, wav := filepath.Join(dir, "q.aiff"), filepath.Join(dir, "q.wav")
	if out, err := exec.Command("say", "-o", aiff, text).CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	if out, err := exec.Command("afconvert", "-f", "WAVE", "-d", "LEI16@24000", "-c", "1", aiff, wav).CombinedOutput(); err != nil {
		t.Fatalf("afconvert: %v %s", err, out)
	}
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	for off := 12; off+8 <= len(b); {
		id, size := string(b[off:off+4]), int(binary.LittleEndian.Uint32(b[off+4:off+8]))
		body := off + 8
		if id == "data" {
			pcm, _ := audio.DecodeLE(b[body:min(body+size, len(b))])
			return pcm
		}
		off = body + size + size%2
	}
	t.Fatal("no data chunk")
	return nil
}

type probe struct {
	t     *testing.T
	conn  *websocket.Conn
	ctx   context.Context
	start time.Time
	out   *os.File

	mu         sync.Mutex
	mic        []int16
	micDoneAt  time.Time // when the last queued speech sample was sent
	seq        int
	delegs     []string // client delegation ids, in order
	loudFrames int
	lastLoud   time.Time
	outText    strings.Builder
}

func (p *probe) ms() int64 { return time.Since(p.start).Milliseconds() }

func (p *probe) log(dir string, v map[string]any) {
	v["t_ms"] = p.ms()
	v["dir"] = dir
	b, _ := json.Marshal(v)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.out != nil {
		_, _ = p.out.Write(append(b, '\n'))
	}
	if dir == "note" {
		p.t.Log(string(b))
	}
}

func (p *probe) send(v map[string]any) {
	p.mu.Lock()
	p.seq++
	v["event_id"] = fmt.Sprintf("p_%d", p.seq)
	p.mu.Unlock()
	if v["type"] != "session.input_audio.append" {
		p.log("send", map[string]any{"msg": v})
	}
	b, _ := json.Marshal(v)
	_ = p.conn.Write(p.ctx, websocket.MessageText, b)
}

func (p *probe) say(pcm []int16) {
	p.mu.Lock()
	p.mic = append(p.mic, pcm...)
	p.mu.Unlock()
	p.log("note", map[string]any{"say_ms": len(pcm) * 1000 / audio.SampleRate})
}

// micLoop streams 40 ms frames in real time: queued speech, else silence.
func (p *probe) micLoop() {
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-tick.C:
		}
		frame := make([]int16, audio.FrameSamples)
		p.mu.Lock()
		n := copy(frame, p.mic)
		p.mic = p.mic[n:]
		if n > 0 && len(p.mic) == 0 {
			p.micDoneAt = time.Now()
			p.mu.Unlock()
			p.log("note", map[string]any{"speech_end": true})
		} else {
			p.mu.Unlock()
		}
		p.send(map[string]any{"type": "session.input_audio.append", "audio": base64.StdEncoding.EncodeToString(audio.EncodeLE(frame))})
	}
}

func (p *probe) readLoop(done chan<- struct{}) {
	defer close(done)
	for {
		_, data, err := p.conn.Read(p.ctx)
		if err != nil {
			p.log("note", map[string]any{"read_end": err.Error()})
			return
		}
		var ev map[string]any
		_ = json.Unmarshal(data, &ev)
		typ, _ := ev["type"].(string)
		switch typ {
		case "session.output_audio.delta":
			raw, _ := base64.StdEncoding.DecodeString(ev["delta"].(string))
			pcm, _ := audio.DecodeLE(raw)
			if voice.RMS(pcm) >= 0.01 {
				p.mu.Lock()
				if p.loudFrames == 0 || time.Since(p.lastLoud) > 700*time.Millisecond {
					p.mu.Unlock()
					p.log("recv", map[string]any{"type": "LOUD_AUDIO_START"})
					p.mu.Lock()
				}
				p.loudFrames++
				p.lastLoud = time.Now()
				p.mu.Unlock()
			}
			continue
		case "session.delegation.created":
			if d, ok := ev["delegation"].(map[string]any); ok {
				if id, ok := d["id"].(string); ok {
					p.mu.Lock()
					p.delegs = append(p.delegs, id)
					p.mu.Unlock()
				}
			}
		}
		p.log("recv", map[string]any{"msg": ev})
		if typ == "session.closed" {
			return
		}
	}
}

func (p *probe) lastDeleg() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.delegs) == 0 {
		return ""
	}
	return p.delegs[len(p.delegs)-1]
}

func (p *probe) waitDeleg(n int, within time.Duration) string {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got := len(p.delegs)
		p.mu.Unlock()
		if got >= n {
			return p.lastDeleg()
		}
		time.Sleep(50 * time.Millisecond)
	}
	p.log("note", map[string]any{"no_delegation_within_ms": within.Milliseconds()})
	return ""
}

const probeInstructions = `You are Bit, a friendly desk companion voice assistant. Keep replies to one or two short sentences.
You have no tools of your own. A capable agent on the user's computer can do real work: check facts, live information like the weather or news, the user's files, projects, builds and code, timers and music. Delegate whenever the user asks for any of that, and never guess while you wait. Answer small talk and general knowledge yourself.
The current local time and date are given to you as context; answer questions about the time from it without delegating.
Delegation policy: when you delegate, say a very short acknowledgement. Results arrive later as commentary starting with "Agent reply:"; tell the user about it in one or two sentences.`

func TestLiveProbeClientDelegation(t *testing.T) {
	if os.Getenv("PROBE_GPTLIVE") == "" {
		t.Skip("set PROBE_GPTLIVE=1")
	}
	cfgDir, _ := os.UserConfigDir()
	key, ok := voice.NewEnvSecrets(voice.DotenvPaths(filepath.Join(cfgDir, "com.sihekuang.deskpixie"))).APIKey("openai-live")
	if !ok {
		t.Skip("no OPENAI_API_KEY")
	}
	build := probeSpeech(t, "Hey Bit, can you check why my build failed?")
	weather := probeSpeech(t, "What's the weather like in Paris right now?")
	timeQ := probeSpeech(t, "What time is it?")
	capital := probeSpeech(t, "What is the capital of Australia?")
	bye := probeSpeech(t, "Thanks, that's all. Bye!")

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "wss://api.openai.com/v1/live/sessions", &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + key.Expose()}},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(16 << 20)
	p := &probe{t: t, conn: conn, ctx: ctx, start: time.Now()}
	if path := os.Getenv("PROBE_OUT"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		p.out = f
	}
	p.send(map[string]any{"type": "session.start", "session": map[string]any{
		"model": "gpt-live-1", "instructions": probeInstructions,
		"audio":      map[string]any{"format": map[string]any{"type": "audio/pcm", "rate": 24000}, "output": map[string]any{"voice": "marin"}},
		"delegation": map[string]any{"type": "client"}, "store": false,
	}})
	done := make(chan struct{})
	go p.readLoop(done)
	time.Sleep(1500 * time.Millisecond) // session.started
	go p.micLoop()
	now := time.Now()
	p.send(map[string]any{"type": "session.thinking.append", "delegation_id": nil,
		"content": "Local time now: " + now.Format("Monday, 2 January 2006, 15:04") + " (" + now.Format("MST, UTC-07:00") + ")."})
	time.Sleep(time.Second)

	// 1. Work task → commentary ack with the delegation id.
	p.say(build)
	d1 := p.waitDeleg(1, 15*time.Second)
	time.Sleep(1500 * time.Millisecond) // does it talk while waiting?
	if d1 != "" {
		p.send(map[string]any{"type": "session.commentary.append", "delegation_id": d1,
			"content": "Handed to the agent as job 7. Tell the user in a few words that you've passed it on and will let them know."})
	}
	time.Sleep(8 * time.Second)

	// 2. Live info → thinking with the delegation id (is it spoken?).
	p.say(weather)
	d2 := p.waitDeleg(2, 15*time.Second)
	time.Sleep(1500 * time.Millisecond)
	if d2 != "" {
		p.send(map[string]any{"type": "session.thinking.append", "delegation_id": d2,
			"content": "Weather in Paris now: 17 degrees Celsius, light rain, wind 12 km/h."})
	}
	time.Sleep(8 * time.Second)

	// 3. Time (should be answered from the thinking context) and general knowledge.
	p.say(timeQ)
	time.Sleep(7 * time.Second)
	p.say(capital)
	time.Sleep(7 * time.Second)

	// 4. Late result for delegation 1 (≈40 s after it), with its id, then one with null.
	if d1 != "" {
		p.send(map[string]any{"type": "session.commentary.append", "delegation_id": d1,
			"content": "Agent reply: the build failed because TestTmuxSessions timed out in core/tmux; it looks flaky."})
	}
	time.Sleep(9 * time.Second)
	p.send(map[string]any{"type": "session.commentary.append", "delegation_id": nil,
		"content": "Agent reply: job 7 also found the lint step passes; only that one test failed."})
	time.Sleep(9 * time.Second)
	// 5. An unknown delegation id: what error?
	p.send(map[string]any{"type": "session.thinking.append", "delegation_id": "dlg_doesnotexist", "content": "test"})
	time.Sleep(2 * time.Second)
	// 6. Unanswered delegation: what does it say while nothing comes back? (15 s)
	p.say(probeSpeech(t, "Can you look at my calendar for tomorrow?"))
	p.waitDeleg(3, 15*time.Second)
	time.Sleep(15 * time.Second)
	// 7. Goodbye.
	p.say(bye)
	time.Sleep(7 * time.Second)
	p.send(map[string]any{"type": "session.close"})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	cancel()
}
