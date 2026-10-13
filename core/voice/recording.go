package voice

// Debug recording of the microphone: what the voice model is sent, saved as WAV files.
// Port of ai-mascot crates/voice-core/src/recording.rs.
//
// Rust wraps the AudioCapture; here the runtime hands each conversation's frames to a
// MicTake (RuntimeConfig.RecordDir). Frames are queued and written on a goroutine of their
// own, so a slow disk never holds up the voice model. A conversation's file is created on its
// first frame, so a mic that never starts leaves none. Unfiltered frames (the mic before the
// noise filter) go to a matching "…-original.wav".

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/sihekuang/deskpixie/core/audio"
)

// wavHeaderEvery is how often the WAV header is brought up to date, in writes: about every
// 2 s of 40 ms frames. A file cut off by a crash still plays, minus at most that much.
const wavHeaderEvery = 50

// micTakeQueue bounds a take's hand-off: beyond it frames are dropped from the recording
// (never from the conversation).
const micTakeQueue = 512

// RecordingDir is <appData>/recordings when VOICE_DEBUG is set, else "" (recording off).
func RecordingDir(appDataDir string) string {
	if _, ok := os.LookupEnv("VOICE_DEBUG"); !ok || appDataDir == "" {
		return ""
	}
	return filepath.Join(appDataDir, "recordings")
}

// WavWriter writes 16-bit mono PCM at 24 kHz as it arrives.
type WavWriter struct {
	f         *os.File
	w         *bufio.Writer
	dataBytes uint32
	writes    uint32
}

// CreateWav fails rather than overwrite an existing file.
func CreateWav(path string) (*WavWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	w := &WavWriter{f: f, w: bufio.NewWriter(f)}
	h := wavHeader(0)
	if _, err := w.w.Write(h[:]); err != nil {
		f.Close()
		return nil, err
	}
	return w, nil
}

// Write appends samples.
func (w *WavWriter) Write(samples []int16) error {
	if err := binary.Write(w.w, binary.LittleEndian, samples); err != nil {
		return err
	}
	n := uint64(w.dataBytes) + uint64(len(samples))*2
	w.dataBytes = uint32(min(n, uint64(^uint32(0)-36)))
	w.writes++
	if w.writes%wavHeaderEvery == 0 {
		return w.SyncHeader()
	}
	return nil
}

// SyncHeader writes out everything buffered and makes the header match it.
func (w *WavWriter) SyncHeader() error {
	if err := w.w.Flush(); err != nil {
		return err
	}
	h := wavHeader(w.dataBytes)
	_, err := w.f.WriteAt(h[:], 0)
	return err
}

// Close syncs the header and closes the file.
func (w *WavWriter) Close() error {
	err := w.SyncHeader()
	return errors.Join(err, w.f.Close())
}

func wavHeader(dataBytes uint32) [44]byte {
	var h [44]byte
	le := binary.LittleEndian
	riff := uint32(min(uint64(dataBytes)+36, uint64(^uint32(0))))
	copy(h[0:4], "RIFF")
	le.PutUint32(h[4:8], riff)
	copy(h[8:12], "WAVE")
	copy(h[12:16], "fmt ")
	le.PutUint32(h[16:20], 16)
	le.PutUint16(h[20:22], 1) // PCM
	le.PutUint16(h[22:24], 1) // mono
	le.PutUint32(h[24:28], audio.SampleRate)
	le.PutUint32(h[28:32], audio.SampleRate*2)
	le.PutUint16(h[32:34], 2)  // block align
	le.PutUint16(h[34:36], 16) // bits per sample
	copy(h[36:40], "data")
	le.PutUint32(h[40:44], dataBytes)
	return h
}

// newRecording creates dir/mic-<ms since 1970>.wav, or …-1.wav, …-2.wav if taken. It also
// returns the path without extension, to name the files that go with it.
func newRecording(dir string) (stem, path string, w *WavWriter, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", nil, err
	}
	millis := strconv.FormatInt(time.Now().UnixMilli(), 10)
	for n := 0; ; n++ {
		stem = filepath.Join(dir, "mic-"+millis)
		if n > 0 {
			stem += "-" + strconv.Itoa(n)
		}
		path = stem + ".wav"
		w, err = CreateWav(path)
		if err == nil {
			return stem, path, w, nil
		}
		if !errors.Is(err, fs.ErrExist) || n >= 100 {
			return "", "", nil, err
		}
	}
}

// MicRecorder saves each conversation's mic audio under a folder.
type MicRecorder struct{ dir string }

// NewMicRecorder records into dir (created on the first frame).
func NewMicRecorder(dir string) *MicRecorder { return &MicRecorder{dir: dir} }

type takeFrame struct {
	samples    []int16
	unfiltered bool
}

// MicTake is one conversation's recording.
type MicTake struct {
	ch   chan takeFrame
	done chan struct{}
}

// Begin starts a conversation's recording; nothing is created until its first frame.
func (m *MicRecorder) Begin() *MicTake {
	t := &MicTake{ch: make(chan takeFrame, micTakeQueue), done: make(chan struct{})}
	go t.run(m.dir)
	return t
}

// Sent records a frame as the model was sent it. Never blocks; the samples are copied.
func (t *MicTake) Sent(samples []int16) { t.push(samples, false) }

// Unfiltered records a frame as the mic heard it, before the noise filter.
func (t *MicTake) Unfiltered(samples []int16) { t.push(samples, true) }

func (t *MicTake) push(samples []int16, unfiltered bool) {
	select {
	case t.ch <- takeFrame{append([]int16(nil), samples...), unfiltered}:
	default:
	}
}

// End finishes the take; the returned channel closes once its files are complete.
// Sent/Unfiltered must not be called after End.
func (t *MicTake) End() <-chan struct{} {
	close(t.ch)
	return t.done
}

// track is one file of a take; nil writer once writing failed.
type track struct {
	path string
	w    *WavWriter
}

func openTrack(path string, w *WavWriter, err error) *track {
	if err != nil {
		log.Printf("[voice] can't record the mic to %s: %v", path, err)
		return &track{path: path}
	}
	log.Printf("[voice] recording the mic to %s", path)
	return &track{path: path, w: w}
}

func (tr *track) write(s []int16) {
	if tr.w == nil {
		return
	}
	if err := tr.w.Write(s); err != nil {
		log.Printf("[voice] stopped recording %s: %v", tr.path, err)
		tr.w.Close()
		tr.w = nil
	}
}

func (tr *track) close() {
	if tr != nil && tr.w != nil {
		tr.w.Close()
	}
}

func (t *MicTake) run(dir string) {
	defer close(t.done)
	var (
		started        bool
		failed         bool
		stem           string
		sent, original *track
	)
	defer func() { sent.close(); original.close() }()
	for f := range t.ch {
		if !started {
			started = true
			s, path, w, err := newRecording(dir)
			if err != nil {
				log.Printf("[voice] can't record the mic in %s: %v", dir, err)
				failed = true
			} else {
				stem = s
				sent = openTrack(path, w, nil)
			}
		}
		if failed {
			continue
		}
		if f.unfiltered {
			if original == nil {
				path := fmt.Sprintf("%s-original.wav", stem)
				w, err := CreateWav(path)
				original = openTrack(path, w, err)
			}
			original.write(f.samples)
		} else {
			sent.write(f.samples)
		}
	}
}
