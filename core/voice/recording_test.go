package voice

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Port of the 9 #[test]s in ai-mascot crates/voice-core/src/recording.rs.
//
// Rust wraps the AudioCapture (RecordingCapture) and tees CaptureEvents; in Go the mic is
// pushed into the runtime (PushMic), so the runtime hands each conversation's frames to a
// MicTake instead. The tee tests therefore check the take here (what lands in the files) and
// runtime_parity_test.go checks that the frames still reach the model
// (TestRecordingFramesReachTheModelAndTheFile, TestRecordingUnwritableFolderStillPassesTheMicThrough).

func wavs(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, filepath.Join(dir, e.Name()))
	}
	slices.Sort(out)
	return out
}

func le(samples ...int16) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("recording did not finish")
	}
}

func TestTheHeaderDescribes24kHzMono16BitPCM(t *testing.T) {
	h := wavHeader(480)
	u32 := func(b []byte) uint32 { return binary.LittleEndian.Uint32(b) }
	u16 := func(b []byte) uint16 { return binary.LittleEndian.Uint16(b) }
	if string(h[0:4]) != "RIFF" || u32(h[4:8]) != 36+480 || string(h[8:16]) != "WAVEfmt " {
		t.Fatalf("riff %q", h[:16])
	}
	if u16(h[22:24]) != 1 || u32(h[24:28]) != 24000 || u16(h[34:36]) != 16 || u32(h[40:44]) != 480 {
		t.Fatal("fmt fields")
	}
	big := wavHeader(^uint32(0))
	if u32(big[4:8]) != ^uint32(0) {
		t.Fatal("overflow")
	}
}

func TestTheFileIsCompleteOnceTheWriterIsDone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	w, err := CreateWav(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Write([]int16{1, -2, 3})
	_ = w.Write([]int16{32767})
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	h := wavHeader(8)
	if !bytes.Equal(b[:44], h[:]) {
		t.Fatal("header")
	}
	if !bytes.Equal(b[44:], []byte{1, 0, 0xfe, 0xff, 3, 0, 0xff, 0x7f}) {
		t.Fatalf("data %v", b[44:])
	}
}

func TestTheHeaderCatchesUpWhileRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.wav")
	w, err := CreateWav(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	frame := make([]int16, 480)
	for i := range frame {
		frame[i] = 5
	}
	for range wavHeaderEvery {
		_ = w.Write(frame)
	}
	b, _ := os.ReadFile(path)
	data := uint32(wavHeaderEvery * 960)
	h := wavHeader(data)
	if !bytes.Equal(b[:44], h[:]) {
		t.Fatal("playable even if the app dies now")
	}
	if len(b) != 44+int(data) {
		t.Fatalf("len %d", len(b))
	}
}

func TestAnExistingFileIsNeverOverwritten(t *testing.T) {
	dir := t.TempDir()
	_, first, a, err := newRecording(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, second, b, err := newRecording(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("same path twice")
	}
	a.Close()
	b.Close()
	if n := len(wavs(t, dir)); n != 2 {
		t.Fatalf("%d files", n)
	}
}

func TestFramesReachTheFile(t *testing.T) {
	// Rust: frames_reach_the_runtime_and_the_file (file half).
	dir := t.TempDir()
	take := NewMicRecorder(dir).Begin()
	take.Sent([]int16{7, 8})
	waitDone(t, take.End())
	files := wavs(t, dir)
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	b, _ := os.ReadFile(files[0])
	if !bytes.Equal(b[44:], le(7, 8)) {
		t.Fatalf("data %v", b[44:])
	}
	if !strings.HasPrefix(filepath.Base(files[0]), "mic-") {
		t.Fatalf("name %s", files[0])
	}
}

func TestTheUnfilteredMicGoesToAMatchingFile(t *testing.T) {
	// Rust: the_unfiltered_mic_goes_to_a_matching_file_and_not_to_the_model (file half).
	dir := t.TempDir()
	take := NewMicRecorder(dir).Begin()
	take.Unfiltered([]int16{9})
	take.Sent([]int16{-9})
	waitDone(t, take.End())
	files := wavs(t, dir)
	if len(files) != 2 {
		t.Fatalf("files %v", files)
	}
	original, sent := files[0], files[1]
	if filepath.Base(original) != strings.Replace(filepath.Base(sent), ".wav", "-original.wav", 1) {
		t.Fatalf("the pair share a name: %s %s", original, sent)
	}
	ob, _ := os.ReadFile(original)
	sb, _ := os.ReadFile(sent)
	if !bytes.Equal(ob[44:], le(9)) || !bytes.Equal(sb[44:], le(-9)) {
		t.Fatal("contents")
	}
}

func TestEachConversationGetsItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	rec := NewMicRecorder(dir)
	for range 2 {
		take := rec.Begin()
		take.Sent([]int16{1})
		waitDone(t, take.End())
	}
	if n := len(wavs(t, dir)); n != 2 {
		t.Fatalf("%d files", n)
	}
}

func TestAMicThatNeverStartsLeavesNoFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "recordings")
	take := NewMicRecorder(dir).Begin()
	waitDone(t, take.End())
	if files := wavs(t, dir); len(files) != 0 {
		t.Fatalf("files %v", files)
	}
}

func TestAnUnwritableFolderNeverBlocksTheMic(t *testing.T) {
	// Rust: an_unwritable_folder_still_passes_the_mic_through (recorder half).
	base := filepath.Join(t.TempDir(), "blocked")
	_ = os.WriteFile(base, []byte("a file, not a folder"), 0o600)
	take := NewMicRecorder(filepath.Join(base, "sub")).Begin()
	start := time.Now()
	for range 2000 {
		take.Sent(make([]int16, 960))
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("Sent took %v", d)
	}
	waitDone(t, take.End())
}
