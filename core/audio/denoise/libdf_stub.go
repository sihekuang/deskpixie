//go:build !deepfilter

package denoise

import "errors"

// LibdfAvailable reports whether this build links libdf (the `deepfilter` build tag).
const LibdfAvailable = false

var errNoLibdf = errors.New("built without libdf (the deepfilter build tag): run make -C core libdf build-dylib")

func loadHush() (Engine, error)       { return nil, errNoLibdf }
func loadDFN3(string) (Engine, error) { return nil, errNoLibdf }
