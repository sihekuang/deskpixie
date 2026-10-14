package voicetest

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Port of the 7 #[tokio::test]s in ai-mascot crates/voice-core/src/conformance.rs, in
// synctest's fake time (tokio's start_paused).

func clips() Clips {
	var c Clips
	for range 5 {
		c.Question = append(c.Question, Tone(FrameMs, 0.3))
		c.Goodbye = append(c.Goodbye, Tone(FrameMs, 0.3))
	}
	for range 8 {
		c.Silence = append(c.Silence, Silence(FrameMs))
	}
	return c
}

func cfg() voice.SessionConfig {
	return voice.SessionConfig{Model: "m", Voice: "v", Instructions: "i", TurnDetection: voice.TurnSemantic}
}

func goodScript() []Reply {
	return []Reply{Audio(3), Audio(20), AudioThenTool(1, voice.EndConversationTool)}
}

func TestScriptedModelConforms(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rep, err := Run(NewScriptedModel(goodScript()...), "k", cfg(), clips())
		if err != nil {
			t.Fatalf("conforms: %v", err)
		}
		if len(rep.Steps) != 5 {
			t.Fatalf("steps %v", rep.Steps)
		}
		synctest.Wait()
	})
}

type mode int

const (
	ignoreCancel mode = iota
	toolAfterDone
	neverClose
	noCreated
)

// broken is a scripted model with one deliberate protocol violation.
type broken struct {
	inner *ScriptedModel
	mode  mode
	stop  chan struct{} // ends a neverClose stream at the end of the test
}

func (b *broken) Name() string { return "broken" }

type brokenSession struct {
	inner  voice.Session
	mode   mode
	events chan voice.Event
}

func (s *brokenSession) Events() <-chan voice.Event { return s.events }
func (s *brokenSession) Close() error               { return s.inner.Close() }
func (s *brokenSession) Send(c voice.Command) error {
	if s.mode == ignoreCancel && c.Kind == voice.CmdCancelResponse {
		return nil
	}
	return s.inner.Send(c)
}

func (b *broken) Connect(ctx context.Context, cfg voice.SessionConfig, key string) (voice.Session, error) {
	inner, err := b.inner.Connect(ctx, cfg, key)
	if err != nil {
		return nil, err
	}
	s := &brokenSession{inner: inner, mode: b.mode, events: make(chan voice.Event, 4096)}
	go func() {
		var held *voice.Event
		for e := range inner.Events() {
			switch {
			case b.mode == noCreated && e.Kind == voice.EvResponseCreated:
			case b.mode == toolAfterDone && e.Kind == voice.EvToolCall:
				held = &e
			default:
				s.events <- e
				if e.Kind == voice.EvResponseDone && held != nil {
					s.events <- *held
					held = nil
				}
			}
		}
		if b.mode == neverClose {
			<-b.stop // keep the stream open
		}
		close(s.events)
	}()
	return s, nil
}

func runBroken(t *testing.T, m mode, replies []Reply) string {
	b := &broken{inner: NewScriptedModel(replies...), mode: m, stop: make(chan struct{})}
	defer close(b.stop)
	_, err := Run(b, "k", cfg(), clips())
	if err == nil {
		t.Fatal("a broken model passed")
	}
	return err.Error()
}

func TestAModelThatIgnoresCancelFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if err := runBroken(t, ignoreCancel, []Reply{Audio(3), Audio(100)}); !strings.Contains(err, "ResponseDone after cancel") {
			t.Fatal(err)
		}
	})
}

func TestAToolCallAfterResponseDoneFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if err := runBroken(t, toolAfterDone, goodScript()); !strings.Contains(err, "ResponseDone before ToolCall") {
			t.Fatal(err)
		}
	})
}

func TestAStreamThatOutlivesCloseFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if err := runBroken(t, neverClose, goodScript()); !strings.Contains(err, "did not end") {
			t.Fatal(err)
		}
	})
}

func TestAModelWithoutResponseCreatedFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		if err := runBroken(t, noCreated, goodScript()); !strings.Contains(err, "ResponseCreated") {
			t.Fatal(err)
		}
	})
}

func TestAModelThatNeverAnswersFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, err := Run(NewScriptedModel(Silent()), "k", cfg(), clips())
		if err == nil || !strings.Contains(err.Error(), "AudioDelta") {
			t.Fatal(err)
		}
	})
}

func TestAModelThatCannotConnectFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, err := Run(FailingModel(), "k", cfg(), clips())
		if err == nil || !strings.Contains(err.Error(), "connect failed") {
			t.Fatal(err)
		}
	})
}
