package main

/*
#include <stdlib.h>
#include <stdint.h>

// const-qualified sample pointer, so the generated header reads like the C ABI.
typedef const int16_t dp_const_int16_t;
*/
import "C"

import (
	"errors"
	"unsafe"

	"github.com/sihekuang/deskpixie/core/bridge"
)

// dp_configure result codes.
const (
	configureOK       = 0
	configureNotInit  = 1
	configureBadJSON  = 2
	configureBuildErr = 3
)

//export dp_init
func dp_init() C.int {
	bridge.Init()
	return 0
}

// dp_configure builds the app core from paths; everything else comes from settings.json:
//
//	{"appSupport":"…/Application Support/com.sihekuang.deskpixie",
//	 "bundledPixies":"…/DeskPixie.app/Contents/Resources/pixies",
//	 "designSystem":"…/DeskPixie.app/Contents/Resources/design-system",
//	 "dfn3ModelPath":"…/DeskPixie.app/Contents/Resources/DeepFilterNet3.tar.gz",
//	 "platform":"macos"}
//
// appSupport is required; the others may be "" (no bundled packs, no design system, no DFN3).
// Phase 3's {apiKey, model, …, packRoots} is still accepted (see bridge.Config).
// Returns 0 ok, 1 not initialized, 2 bad JSON / no appSupport, 3 build error (dp_last_error).
//
//export dp_configure
func dp_configure(json *C.char) C.int {
	if json == nil {
		if err := bridge.Configure(nil); errors.Is(err, bridge.ErrNotInit) {
			return configureNotInit
		}
		return configureBadJSON
	}
	err := bridge.Configure([]byte(C.GoString(json)))
	switch {
	case err == nil:
		return configureOK
	case errors.Is(err, bridge.ErrNotInit):
		return configureNotInit
	case errors.Is(err, bridge.ErrBadJSON):
		return configureBadJSON
	default:
		return configureBuildErr
	}
}

//export dp_hotkey
func dp_hotkey() { bridge.Hotkey() }

// dp_push_audio copies count samples; 0 = accepted, 1 = dropped.
//
//export dp_push_audio
func dp_push_audio(samples *C.dp_const_int16_t, count C.int, captured_unix_nanos C.int64_t) C.int {
	if samples == nil || count <= 0 {
		return 1
	}
	s := unsafe.Slice((*int16)(unsafe.Pointer(samples)), int(count))
	if bridge.PushAudio(s, int64(captured_unix_nanos)) {
		return 0
	}
	return 1
}

// dp_pull_audio writes up to max samples into dst and returns how many; *flush is set to
// 1 when Swift must drop its buffered playback.
//
//export dp_pull_audio
func dp_pull_audio(dst *C.int16_t, max C.int, flush *C.int) C.int {
	if flush != nil {
		*flush = 0
	}
	if dst == nil || max <= 0 {
		return 0
	}
	buf := unsafe.Slice((*int16)(unsafe.Pointer(dst)), int(max))
	n, fl := bridge.PullAudio(buf)
	if fl && flush != nil {
		*flush = 1
	}
	return C.int(n)
}

//export dp_report_playback
func dp_report_playback(played C.int64_t, buffered C.int, rms C.float, underruns C.int64_t) {
	bridge.ReportPlayback(int64(played), int(buffered), float32(rms), int64(underruns))
}

//export dp_mic_failed
func dp_mic_failed(reason *C.char) {
	r := ""
	if reason != nil {
		r = C.GoString(reason)
	}
	bridge.MicFailed(r)
}

// dp_poll_event returns one UI event as JSON, or NULL. Free with dp_free_string.
//
//export dp_poll_event
func dp_poll_event() *C.char { return cString(bridge.PollEvent()) }

// dp_pack_json returns {"pack":...,"sheetPath":...} or NULL. Free with dp_free_string.
//
//export dp_pack_json
func dp_pack_json() *C.char { return cString(bridge.PackJSON()) }

// dp_last_error returns the last configure error or NULL. Free with dp_free_string.
//
//export dp_last_error
func dp_last_error() *C.char {
	if s := bridge.LastError(); s != "" {
		return C.CString(s)
	}
	return nil
}

// dp_call runs a registered core method. args_json may be NULL (= {}). It returns
// {"ok":<json>} or {"error":"<msg>"} (never NULL). Free with dp_free_string.
//
//export dp_call
func dp_call(method *C.char, args_json *C.char) *C.char {
	if method == nil {
		return cString([]byte(`{"error":"missing method"}`))
	}
	var args []byte
	if args_json != nil {
		args = []byte(C.GoString(args_json))
	}
	return cString(bridge.Call(C.GoString(method), args))
}

//export dp_free_string
func dp_free_string(s *C.char) {
	if s != nil {
		C.free(unsafe.Pointer(s))
	}
}

//export dp_destroy
func dp_destroy() { bridge.Destroy() }

func cString(b []byte) *C.char {
	if b == nil {
		return nil
	}
	return (*C.char)(C.CBytes(append(b, 0)))
}
