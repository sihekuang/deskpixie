package settings

import (
	"reflect"
	"testing"
)

// #221: text mode is a Pixie setting, voice by default.
func TestReplyModeDefaultsToVoice(t *testing.T) {
	if got := Default().ReplyMode; got != ReplyVoice || ReplyVoice != "voice" || ReplyText != "text" {
		t.Fatal(got)
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.ReplyMode != ReplyVoice {
		t.Fatal("an old settings.json without the key is voice:", c.ReplyMode, err)
	}
}

func TestReplyModeIsALivePixieChoice(t *testing.T) {
	f := FieldByPath("replyMode")
	if f.Section != SectionPixie || f.Apply != ApplyLive {
		t.Fatalf("%+v", f)
	}
	var vals []string
	for _, o := range ReplyModes {
		vals = append(vals, o.Value)
	}
	if !reflect.DeepEqual(vals, []string{"voice", "text"}) {
		t.Fatal(vals)
	}
	if r, ok := RouteFor("replyMode"); !ok || r != RouteReplyMode {
		t.Fatal(r, ok)
	}
}

func TestReplyModeMustBeVoiceOrText(t *testing.T) {
	env := testEnv(t)
	for mode, ok := range map[string]bool{"voice": true, "text": true, "chat": false, "": false} {
		c := Default()
		c.ReplyMode = mode
		got := problems(c, env)
		bad := len(got) == 1 && got[0][0] == "replyMode"
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%q: %v", mode, got)
		}
	}
}

// #224: captions show on hover; a Pixie switch keeps them up all the time. Off by default.
func TestAlwaysShowCaptionIsALivePixieSwitchOffByDefault(t *testing.T) {
	if Default().AlwaysShowCaption {
		t.Fatal("on by default")
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.AlwaysShowCaption {
		t.Fatal("an old settings.json without the key is off:", c.AlwaysShowCaption, err)
	}
	f := FieldByPath("alwaysShowCaption")
	if f.Section != SectionPixie || f.Apply != ApplyLive || f.Kind.Type != KindBool {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("alwaysShowCaption"); !ok || r != RouteCaptions {
		t.Fatal(r, ok)
	}
}

// #224: what Pixie said is dismissed after a while, 2 minutes by default (0 keeps it).
func TestCaptionDismissSecsIsALivePixieNumber(t *testing.T) {
	if got := Default().CaptionDismissSecs; got != 120 {
		t.Fatal(got)
	}
	c, err := ParseConfig([]byte(`{"hotkey":"ctrl+alt+Space"}`))
	if err != nil || c.CaptionDismissSecs != 120 {
		t.Fatal("an old settings.json gets the default:", c.CaptionDismissSecs, err)
	}
	f := FieldByPath("captionDismissSecs")
	if f.Section != SectionPixie || f.Apply != ApplyLive || f.Kind.Type != KindInt {
		t.Fatalf("%+v", f)
	}
	if r, ok := RouteFor("captionDismissSecs"); !ok || r != RouteCaptions {
		t.Fatal(r, ok)
	}
	env := testEnv(t)
	for secs, ok := range map[uint32]bool{0: true, 120: true, 86400: true, 86401: false} {
		c := Default()
		c.CaptionDismissSecs = secs
		got := problems(c, env)
		bad := len(got) == 1 && got[0][0] == "captionDismissSecs"
		if ok && len(got) != 0 || !ok && !bad {
			t.Errorf("%d: %v", secs, got)
		}
	}
}
