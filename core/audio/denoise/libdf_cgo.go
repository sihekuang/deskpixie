//go:build deepfilter

package denoise

/*
#cgo CFLAGS: -I${SRCDIR}/libdf/include
#include <dlfcn.h>
#include <stdlib.h>
#include "deep_filter.h"

// libdf is opened with dlopen, not linked: a build whose app does not bundle libdf.dylib still
// launches, and the filter then passes audio through.
static DFState *(*p_create)(const char *, float);
static uintptr_t (*p_frame_length)(DFState *);
static float (*p_process)(DFState *, float *, float *);
static void (*p_free)(DFState *);

// Returns NULL on success, else dlerror's message (static storage).
static const char *dp_df_open(const char *path) {
	void *h = dlopen(path, RTLD_NOW | RTLD_LOCAL);
	if (!h) return dlerror();
	p_create = (DFState *(*)(const char *, float))dlsym(h, "df_create");
	p_frame_length = (uintptr_t (*)(DFState *))dlsym(h, "df_get_frame_length");
	p_process = (float (*)(DFState *, float *, float *))dlsym(h, "df_process_frame");
	p_free = (void (*)(DFState *))dlsym(h, "df_free");
	if (!p_create || !p_frame_length || !p_process || !p_free) return "libdf lacks the DeepFilterNet C API";
	return NULL;
}
static DFState *dp_df_create(const char *path, float atten) { return p_create(path, atten); }
static uintptr_t dp_df_frame_length(DFState *st) { return p_frame_length(st); }
static float dp_df_process(DFState *st, float *in, float *out) { return p_process(st, in, out); }
static void dp_df_free(DFState *st) { p_free(st); }
*/
import "C"

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sync"
	"unsafe"
)

// LibdfPaths are where libdf.dylib is looked for, in order. The app bundles it in
// Contents/Frameworks, which the executable's @rpath covers; DESKPIXIE_LIBDF overrides.
var LibdfPaths = []string{"@rpath/libdf.dylib", "@loader_path/libdf.dylib", "libdf.dylib"}

var (
	openOnce sync.Once
	openErr  error
)

// openLibdf dlopens libdf once per process.
func openLibdf() error {
	openOnce.Do(func() {
		paths := LibdfPaths
		if p := os.Getenv("DESKPIXIE_LIBDF"); p != "" {
			paths = []string{p}
		}
		var errs []error
		for _, p := range paths {
			cp := C.CString(p)
			msg := C.dp_df_open(cp)
			C.free(unsafe.Pointer(cp))
			if msg == nil {
				return
			}
			errs = append(errs, errors.New(C.GoString(msg)))
		}
		openErr = fmt.Errorf("cannot open libdf.dylib: %w", errors.Join(errs...))
	})
	return openErr
}

// LibdfAvailable reports whether this build links libdf (the `deepfilter` build tag).
const LibdfAvailable = true

// hushTar is Hush (pulp-vision/Hush, Apache-2.0): DeepFilterNet3 retrained at 16 kHz with no
// lookahead and with background speakers 12-24 dB below the user. See models/README.md.
//
//go:embed models/hush16k.tar.gz
var hushTar []byte

// dfEngine is one libdf state (df_create) working in hop-sample hops.
type dfEngine struct {
	st     *C.DFState
	hop    int
	path   string
	temp   bool // path is our temp copy of an embedded model: removed on Close
	atten  float32
	dither dither
	in     []float32
	out    []float32
}

func loadHush() (Engine, error) {
	f, err := os.CreateTemp("", "deskpixie-hush16k-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("cannot stage the Hush model: %w", err)
	}
	_, werr := f.Write(hushTar)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("cannot stage the Hush model: %v", errors.Join(werr, cerr))
	}
	e, err := newDFEngine(f.Name(), true, HushAttenLimitDB, Hop16k)
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return e, err
}

func loadDFN3(path string) (Engine, error) {
	return newDFEngine(path, false, DFN3AttenLimitDB, Hop48k)
}

func newDFEngine(path string, temp bool, atten float32, hop int) (*dfEngine, error) {
	e := &dfEngine{path: path, temp: temp, atten: atten, hop: hop, dither: newDither(),
		in: make([]float32, hop), out: make([]float32, hop)}
	if err := e.create(); err != nil {
		return nil, err
	}
	return e, nil
}

// create makes the libdf state from e.path. libdf's df_create aborts the process (a Rust panic
// across the C ABI) on a missing or unreadable model, so the file is checked first.
func (e *dfEngine) create() error {
	if err := openLibdf(); err != nil {
		return err
	}
	if e.path == "" {
		return errors.New("no model file given")
	}
	fi, err := os.Stat(e.path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() || fi.Size() == 0 {
		return fmt.Errorf("%s is not a model file", e.path)
	}
	cPath := C.CString(e.path)
	defer C.free(unsafe.Pointer(cPath))
	st := C.dp_df_create(cPath, C.float(e.atten))
	if st == nil {
		return fmt.Errorf("libdf could not load %s", e.path)
	}
	if got := int(C.dp_df_frame_length(st)); got != e.hop {
		C.dp_df_free(st)
		return fmt.Errorf("unexpected model format: %d-sample hops, want %d", got, e.hop)
	}
	e.st = st
	return nil
}

func (e *dfEngine) Hop() int { return e.hop }

func (e *dfEngine) ProcessHop(noisy, clean []float32) error {
	if e.st == nil {
		return errors.New("the model is closed")
	}
	copy(e.in, noisy)
	e.dither.apply(e.in)
	C.dp_df_process(e.st, (*C.float)(unsafe.Pointer(&e.in[0])), (*C.float)(unsafe.Pointer(&e.out[0])))
	copy(clean, e.out)
	return nil
}

// Reset reloads the model: libdf's C API has no reset, and its state keeps STFT and network
// history (the Rust version restored a pristine clone instead).
func (e *dfEngine) Reset() error {
	if e.st != nil {
		C.dp_df_free(e.st)
		e.st = nil
	}
	e.dither = newDither()
	return e.create()
}

func (e *dfEngine) Close() error {
	if e.st != nil {
		C.dp_df_free(e.st)
		e.st = nil
	}
	if e.temp && e.path != "" {
		_ = os.Remove(e.path)
		e.path = ""
	}
	return nil
}
