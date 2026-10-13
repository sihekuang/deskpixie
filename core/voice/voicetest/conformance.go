package voicetest

// What every voice.Model adapter must do (spec §8.3). Runs against fakes in tests and
// against real providers in smoke runs. Port of ai-mascot crates/voice-core/src/conformance.rs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sihekuang/deskpixie/core/voice"
)

// Clips are the utterances the suite sends, as frames.
type Clips struct {
	// Question: a spoken question with a clear answer. It must make the model answer for
	// several seconds (live: "Tell me about the planets"), so the reply is still streaming
	// when the cancel is sent.
	Question [][]int16
	// Goodbye: a clear goodbye (live: "Thanks, that's all, bye!").
	Goodbye [][]int16
	// Silence: trailing silence after each utterance so end-of-speech detection fires.
	Silence [][]int16
}

// Report is what a passing run saw.
type Report struct {
	// Transcripts are the assistant transcripts, in order.
	Transcripts []string
	// Steps lists the checks that passed.
	Steps []string
	// ResponseCreated / ResponseDone seen so far, for the bookkeeping checks.
	created, done int
}

// Timeouts, verbatim from Rust.
const (
	ConnectTimeout = 15 * time.Second
	TurnTimeout    = 30 * time.Second
	CloseTimeout   = 5 * time.Second
	// CancelTimeout: how long a cancelled reply may take to report ResponseDone.
	CancelTimeout = 3 * time.Second
	// CancelSettle: after a cancelled reply is done, no audio may follow within this window.
	CancelSettle = 300 * time.Millisecond
	// Quiet: silence that counts as "the model has stopped talking".
	Quiet      = 500 * time.Millisecond
	QuietBound = 5 * time.Second
)

type matcher func(voice.Event) bool

func is(k voice.EventKind) matcher { return func(e voice.Event) bool { return e.Kind == k } }

// Run runs the checks against model. It returns the report, or an error naming the first
// check that failed.
func Run(model voice.Model, key string, cfg voice.SessionConfig, clips Clips) (*Report, error) {
	rep := &Report{}
	ctx, cancel := context.WithTimeout(context.Background(), ConnectTimeout)
	type res struct {
		s   voice.Session
		err error
	}
	got := make(chan res, 1)
	go func() {
		s, err := model.Connect(ctx, cfg, key)
		got <- res{s, err}
	}()
	var s voice.Session
	select {
	case r := <-got:
		cancel()
		if r.err != nil {
			return nil, fmt.Errorf("connect failed: %v", r.err)
		}
		s = r.s
	case <-time.After(ConnectTimeout):
		cancel()
		go func() { // close whatever arrives late
			if r := <-got; r.err == nil {
				_ = r.s.Close()
			}
		}()
		return nil, errors.New("connect timed out")
	}
	closed := false
	defer func() {
		if !closed {
			go func() { _ = s.Close() }()
		}
	}()
	if _, err := expect(s, rep, ConnectTimeout, "Ready", is(voice.EvReady)); err != nil {
		return nil, err
	}
	rep.Steps = append(rep.Steps, "connect → Ready")

	// 1. A full turn.
	if err := sendAll(s, clips.Question, clips.Silence); err != nil {
		return nil, err
	}
	for _, step := range []struct {
		what string
		k    voice.EventKind
	}{{"SpeechStarted", voice.EvSpeechStarted}, {"SpeechStopped", voice.EvSpeechStopped},
		{"AudioDelta", voice.EvAudioDelta}, {"ResponseDone", voice.EvResponseDone}} {
		if _, err := expect(s, rep, TurnTimeout, step.what, is(step.k)); err != nil {
			return nil, err
		}
	}
	rep.Steps = append(rep.Steps, "speech → SpeechStarted, SpeechStopped, AudioDelta, ResponseDone")

	// 2. Cancelling a reply ends it.
	if err := sendAll(s, clips.Question, clips.Silence); err != nil {
		return nil, err
	}
	if _, err := expect(s, rep, TurnTimeout, "AudioDelta (second turn)", is(voice.EvAudioDelta)); err != nil {
		return nil, err
	}
	if err := s.Send(voice.Command{Kind: voice.CmdCancelResponse, HeardMs: 200}); err != nil {
		return nil, errors.New("session closed before cancel")
	}
	if _, err := expect(s, rep, CancelTimeout, "ResponseDone after cancel", is(voice.EvResponseDone)); err != nil {
		return nil, err
	}
	late, err := drain(s, rep, CancelSettle)
	if err != nil {
		return nil, err
	}
	for _, e := range late {
		if e.Kind == voice.EvAudioDelta {
			return nil, errors.New("AudioDelta after cancel")
		}
	}
	rep.Steps = append(rep.Steps, "CancelResponse → ResponseDone")

	// 3. Tool calls arrive before their response's ResponseDone and accept a result.
	if err := quiesce(s, rep); err != nil {
		return nil, err
	}
	if err := sendAll(s, clips.Goodbye, clips.Silence); err != nil {
		return nil, err
	}
	call, err := expectBefore(s, rep, TurnTimeout, "ToolCall end_conversation",
		func(e voice.Event) bool { return e.Kind == voice.EvToolCall && e.ToolName == voice.EndConversationTool },
		func(e voice.Event) string {
			if e.Kind == voice.EvResponseDone {
				return "ResponseDone before ToolCall"
			}
			return ""
		})
	if err != nil {
		return nil, err
	}
	if _, err := expect(s, rep, TurnTimeout, "ResponseDone after ToolCall", is(voice.EvResponseDone)); err != nil {
		return nil, err
	}
	if err := s.Send(voice.Command{Kind: voice.CmdToolResult, CallID: call.CallID, Output: json.RawMessage(`{"ok":true}`)}); err != nil {
		return nil, errors.New("session closed before tool result")
	}
	rep.Steps = append(rep.Steps, "goodbye → ToolCall(end_conversation) before ResponseDone")

	// 4. Close ends the event stream.
	closed = true
	go func() { _ = s.Close() }()
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		for range s.Events() {
		}
	}()
	select {
	case <-ended:
	case <-time.After(CloseTimeout):
		return nil, errors.New("event stream did not end after Close")
	}
	rep.Steps = append(rep.Steps, "Close → event stream ends")
	return rep, nil
}

// sendAll sends the frames with real-time pacing, like a microphone.
func sendAll(s voice.Session, clips ...[][]int16) error {
	for _, frames := range clips {
		for _, f := range frames {
			if err := s.Send(voice.Command{Kind: voice.CmdAudio, Audio: f}); err != nil {
				return errors.New("session closed while sending audio")
			}
			time.Sleep(voice.FrameDuration(f))
		}
	}
	return nil
}

// track updates the response bookkeeping and the transcripts for one event.
func track(rep *Report, e voice.Event) error {
	switch {
	case e.Kind == voice.EvResponseCreated:
		rep.created++
	case e.Kind == voice.EvAudioDelta && rep.created <= rep.done:
		return errors.New("AudioDelta without an open response (missing ResponseCreated)")
	case e.Kind == voice.EvResponseDone:
		rep.done++
		if rep.done > rep.created {
			return errors.New("ResponseDone without a matching ResponseCreated")
		}
	case e.Kind == voice.EvTranscript && e.Role == "assistant":
		rep.Transcripts = append(rep.Transcripts, e.Text)
	}
	return nil
}

// drain collects (and tracks) every event that arrives within window.
func drain(s voice.Session, rep *Report, window time.Duration) ([]voice.Event, error) {
	var seen []voice.Event
	deadline := time.After(window)
	events := s.Events()
	for {
		select {
		case <-deadline:
			return seen, nil
		case e, ok := <-events:
			if !ok {
				events = nil // stream ended: nothing more will come; wait out the window
				continue
			}
			if err := track(rep, e); err != nil {
				return nil, err
			}
			seen = append(seen, e)
		}
	}
}

// quiesce waits until no event has arrived for Quiet (bounded by QuietBound).
func quiesce(s voice.Session, rep *Report) error {
	start := time.Now()
	for time.Since(start) < QuietBound {
		seen, err := drain(s, rep, Quiet)
		if err != nil {
			return err
		}
		if len(seen) == 0 {
			return nil
		}
	}
	return errors.New("the model never went quiet")
}

// expect skips events until one matches; fails on timeout, stream end or a fatal error.
func expect(s voice.Session, rep *Report, within time.Duration, what string, m matcher) (voice.Event, error) {
	return expectBefore(s, rep, within, what, m, func(voice.Event) string { return "" })
}

// expectBefore is expect, failing with abort's message for an event that must not come first.
func expectBefore(s voice.Session, rep *Report, within time.Duration, what string, m matcher,
	abort func(voice.Event) string) (voice.Event, error) {
	deadline := time.After(within)
	for {
		select {
		case <-deadline:
			return voice.Event{}, fmt.Errorf("timed out waiting for %s", what)
		case e, ok := <-s.Events():
			if !ok {
				return voice.Event{}, fmt.Errorf("session closed while waiting for %s", what)
			}
			if err := track(rep, e); err != nil {
				return voice.Event{}, err
			}
			if e.Kind == voice.EvError && e.Fatal {
				return voice.Event{}, fmt.Errorf("fatal error while waiting for %s: %s", what, e.Message)
			}
			if m(e) {
				return e, nil
			}
			if msg := abort(e); msg != "" {
				return voice.Event{}, errors.New(msg)
			}
		}
	}
}
