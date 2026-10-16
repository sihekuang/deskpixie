// Package wire is the pix ⇄ daemon protocol (#89): one JSON object per line over a Unix
// socket. Both ends ship in the same app, so Version only guards a stale pix link.
package wire

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
)

const Version = 1

// MaxLine bounds one message (a prompt is far smaller).
const MaxLine = 1 << 20

// Kind tells Msg variants apart.
type Kind string

const (
	KindRegister   Kind = "register"
	KindRegistered Kind = "registered"
	KindRefused    Kind = "refused"
	KindEvent      Kind = "event"
	KindCommand    Kind = "command"
	KindResult     Kind = "result"
	KindBye        Kind = "bye"
)

// Msg is one message. Only the fields of its Kind are set: register → Tool…Description;
// registered → Address; refused → Reason; event → Event (status, description, session,
// exited, hook, turn) and its fields; command → Command, Text, Keys; result → Text, Err.
type Msg struct {
	V           int      `json:"v"`
	Kind        Kind     `json:"kind"`
	ID          string   `json:"id,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Pid         int      `json:"pid,omitempty"`
	ProcStart   string   `json:"procStart,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	GitRoot     string   `json:"gitRoot,omitempty"`
	Where       string   `json:"where,omitempty"`
	App         string   `json:"app,omitempty"`
	SessionID   string   `json:"sessionId,omitempty"`
	Description string   `json:"description,omitempty"`
	Address     string   `json:"address,omitempty"`
	Reason      string   `json:"reason,omitempty"`
	Event       string   `json:"event,omitempty"`
	Status      string   `json:"status,omitempty"`
	Source      string   `json:"source,omitempty"`
	ExitCode    int      `json:"exitCode,omitempty"`
	DurationMs  int64    `json:"durationMs,omitempty"`
	Command     string   `json:"command,omitempty"`
	Text        string   `json:"text,omitempty"`
	Keys        []string `json:"keys,omitempty"`
	Err         string   `json:"err,omitempty"`
}

// Encode writes m as one line, with V set.
func Encode(w io.Writer, m Msg) error {
	m.V = Version
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// Decoder reads messages one line at a time.
type Decoder struct{ s *bufio.Scanner }

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 64<<10), MaxLine)
	return &Decoder{s: s}
}

// Next is the next message; io.EOF at the end.
func (d *Decoder) Next() (Msg, error) {
	if !d.s.Scan() {
		if err := d.s.Err(); err != nil {
			return Msg{}, err
		}
		return Msg{}, io.EOF
	}
	var m Msg
	if err := json.Unmarshal(d.s.Bytes(), &m); err != nil {
		return Msg{}, fmt.Errorf("pix wire: %w", err)
	}
	return m, nil
}

// SocketPath is <appData>/agent/pix.sock, or <tmp>/deskpixie-<uid>/pix.sock when that is too
// long for a Unix socket (104 bytes on macOS, with margin).
func SocketPath(appData string, uid int, tmp string) string {
	p := filepath.Join(appData, "agent", "pix.sock")
	if len(p) <= 100 {
		return p
	}
	return filepath.Join(tmp, fmt.Sprintf("deskpixie-%d", uid), "pix.sock")
}
