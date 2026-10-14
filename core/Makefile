.PHONY: build-dylib test test-denoise libdf clean
# Match the app's deployment target, or ld warns "dylib built for newer version".
MACOS_MIN := 14.0

# The noise filter (audio/denoise) runs DeepFilterNet through libdf, opened with dlopen at run
# time. When libdf is present (it is committed; make libdf re-copies it), build-dylib turns on the `deepfilter` tag
# (which also embeds the Hush model) and stages libdf.dylib and the DFN3 model in build/ for
# the app to bundle. Without it the build still works and the filter passes audio through.
LIBDF_DIR := audio/denoise/libdf
LIBDF := $(LIBDF_DIR)/lib/libdf.dylib
DFN3_MODEL := $(LIBDF_DIR)/models/DeepFilterNet3.tar.gz
# Where `make libdf` copies from: Howl's vendored DeepFilterNet v0.5.6 build (see
# audio/denoise/libdf/README.md to build it from source instead).
LIBDF_SRC ?= $(HOME)/Documents/Projects/voice-keyboard/core/third_party/deepfilter
GOTAGS := $(if $(wildcard $(LIBDF)),deepfilter,)

build-dylib:
	MACOSX_DEPLOYMENT_TARGET=$(MACOS_MIN) CGO_CFLAGS="-mmacosx-version-min=$(MACOS_MIN)" CGO_LDFLAGS="-mmacosx-version-min=$(MACOS_MIN)" go build -tags "$(GOTAGS)" -buildmode=c-shared -o build/libdeskpixie.dylib ./cmd/libdeskpixie
	install_name_tool -id "@rpath/libdeskpixie.dylib" build/libdeskpixie.dylib
	@if [ -n "$(GOTAGS)" ]; then \
		cp -pf $(LIBDF) build/libdf.dylib; \
		if [ -f $(DFN3_MODEL) ]; then cp -pf $(DFN3_MODEL) build/DeepFilterNet3.tar.gz; fi; \
		echo "noise filter: built with libdf (bundle build/libdf.dylib and build/DeepFilterNet3.tar.gz)"; \
	else \
		rm -f build/libdf.dylib build/DeepFilterNet3.tar.gz; \
		echo "noise filter: libdf not found ($(LIBDF)); it will pass audio through. See make libdf."; \
	fi

# Re-copy libdf.dylib and the DeepFilterNet3 model into audio/denoise/libdf from Howl (both are
# committed; this refreshes or upgrades them, see audio/denoise/libdf/VERSION.md).
libdf:
	@test -f $(LIBDF_SRC)/lib/macos-arm64/libdf.dylib || (echo "no libdf at $(LIBDF_SRC); see audio/denoise/libdf/README.md" && exit 1)
	mkdir -p $(LIBDF_DIR)/lib $(LIBDF_DIR)/models
	cp -pf $(LIBDF_SRC)/lib/macos-arm64/libdf.dylib $(LIBDF)
	install_name_tool -id "@rpath/libdf.dylib" $(LIBDF)
	cp -pf $(LIBDF_SRC)/models/DeepFilterNet3.tar.gz $(DFN3_MODEL)

test:
	go test ./...

# The noise filter against the real models (needs make libdf).
test-denoise:
	go test -race -tags deepfilter ./audio/denoise/

clean:
	rm -rf build
