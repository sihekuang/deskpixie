package settings

import (
	"fmt"
	"strings"
)

// CheckHotkey reports whether combo is a key combination, by the grammar Rust used
// (tauri-plugin-global-shortcut → global_hotkey::hotkey::parse_hotkey): `+`-separated
// modifiers (alt/option, ctrl/control, shift, super/cmd/command, cmdorctrl and its spellings)
// then exactly one key, case-insensitive; a lone key is allowed. Go only checks the syntax:
// Swift binds the shortcut.
func CheckHotkey(combo string) error {
	tokens := strings.Split(combo, "+")
	if len(tokens) == 1 {
		return checkKey(strings.TrimSpace(tokens[0]))
	}
	haveKey := false
	for _, raw := range tokens {
		token := strings.TrimSpace(raw)
		if token == "" {
			return fmt.Errorf("empty token in %q", combo)
		}
		if haveKey {
			return fmt.Errorf("invalid format %q: the key must come last, once", combo)
		}
		switch strings.ToUpper(token) {
		case "OPTION", "ALT", "CONTROL", "CTRL", "COMMAND", "CMD", "SUPER", "SHIFT",
			"COMMANDORCONTROL", "COMMANDORCTRL", "CMDORCTRL", "CMDORCONTROL":
		default:
			if err := checkKey(token); err != nil {
				return err
			}
			haveKey = true
		}
	}
	if !haveKey {
		return fmt.Errorf("invalid format %q: no key", combo)
	}
	return nil
}

var hotkeyKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range strings.Fields(`BACKQUOTE ` + "`" + ` BACKSLASH \ BRACKETLEFT [ BRACKETRIGHT ] PAUSE PAUSEBREAK
		COMMA , EQUAL = MINUS - PERIOD . QUOTE ' SEMICOLON ; SLASH /
		BACKSPACE CAPSLOCK ENTER SPACE TAB DELETE END HOME INSERT PAGEDOWN PAGEUP PRINTSCREEN
		SCROLLLOCK ARROWDOWN DOWN ARROWLEFT LEFT ARROWRIGHT RIGHT ARROWUP UP NUMLOCK
		NUMPADADD NUMADD NUMPADPLUS NUMPLUS NUMPADDECIMAL NUMDECIMAL NUMPADDIVIDE NUMDIVIDE
		NUMPADENTER NUMENTER NUMPADEQUAL NUMEQUAL NUMPADMULTIPLY NUMMULTIPLY
		NUMPADSUBTRACT NUMSUBTRACT ESCAPE ESC
		AUDIOVOLUMEDOWN VOLUMEDOWN AUDIOVOLUMEUP VOLUMEUP AUDIOVOLUMEMUTE VOLUMEMUTE
		MEDIAPLAY MEDIAPAUSE MEDIAPLAYPAUSE MEDIASTOP MEDIATRACKNEXT MEDIATRACKPREV MEDIATRACKPREVIOUS`) {
		m[k] = true
	}
	for c := 'A'; c <= 'Z'; c++ {
		m[string(c)], m["KEY"+string(c)] = true, true
	}
	for d := '0'; d <= '9'; d++ {
		m[string(d)], m["DIGIT"+string(d)], m["NUMPAD"+string(d)], m["NUM"+string(d)] = true, true, true, true
	}
	for n := 1; n <= 24; n++ {
		m[fmt.Sprintf("F%d", n)] = true
	}
	return m
}()

func checkKey(key string) error {
	if !hotkeyKeys[strings.ToUpper(key)] {
		return fmt.Errorf("unsupported key %q", key)
	}
	return nil
}
