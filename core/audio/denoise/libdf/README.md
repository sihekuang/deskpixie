# libdf (DeepFilterNet C API)

The noise filter (`core/audio/denoise`) runs Hush and DeepFilterNet3 through **libdf**, the C
API of [DeepFilterNet](https://github.com/Rikorose/DeepFilterNet) (MIT OR Apache-2.0), at tag
`v0.5.6` (the version ai-mascot's Rust code used).

- `include/deep_filter.h` is committed (cbindgen output for v0.5.6).
- `lib/libdf.dylib` and `models/DeepFilterNet3.tar.gz` are **not** committed; fetch them with
  `make -C core libdf`.

The Go code opens `libdf.dylib` with `dlopen` (not a link-time dependency), looking at
`@rpath/libdf.dylib`, `@loader_path/libdf.dylib`, then `libdf.dylib`; `DESKPIXIE_LIBDF=<path>`
overrides. If it cannot be opened, the filter logs why once and passes audio through.

## Getting libdf

### From Howl (what `make libdf` does)

Howl (`~/Documents/Projects/voice-keyboard`) vendors a v0.5.6 arm64 build at
`core/third_party/deepfilter/{lib/macos-arm64/libdf.dylib,models/DeepFilterNet3.tar.gz}`.
`make -C core libdf` copies both here (override the source with `LIBDF_SRC=<dir>`).

### From source

Homebrew has no libdf formula; build it with cargo (as Howl's `core/BUILDING_DENOISE.md`):

```bash
cargo install cbindgen
git clone --depth 1 --branch v0.5.6 https://github.com/Rikorose/DeepFilterNet.git
cd DeepFilterNet
cargo update -p time            # the pinned time crate does not build on current rustc
# libDF/Cargo.toml, [lib]: crate-type = ["cdylib", "rlib"]
MACOSX_DEPLOYMENT_TARGET=14.0 cargo build --release -p deep_filter --features capi --target aarch64-apple-darwin
cbindgen --config cbindgen.toml --crate deep_filter --output deep_filter.h
mkdir -p <repo>/core/audio/denoise/libdf/{lib,models}
cp target/aarch64-apple-darwin/release/libdf.dylib <repo>/core/audio/denoise/libdf/lib/
install_name_tool -id @rpath/libdf.dylib <repo>/core/audio/denoise/libdf/lib/libdf.dylib
cp models/DeepFilterNet3_onnx.tar.gz <repo>/core/audio/denoise/libdf/models/DeepFilterNet3.tar.gz
```

## Building and testing

- `make -C core build-dylib` turns on the `deepfilter` tag when `lib/libdf.dylib` exists, and
  stages `build/libdf.dylib` and `build/DeepFilterNet3.tar.gz` for the app to bundle
  (`Contents/Frameworks/libdf.dylib`; the model in `Contents/Resources`, passed as
  `Config.DFN3ModelPath`). The Hush model is embedded in libdeskpixie.
- `make -C core test-denoise` runs every test, including the real-model ones.
- `go test ./audio/denoise/` (no tag) runs the resampling, buffering, dither, switch and
  pass-through tests against fake engines; no libdf needed.

## Caveat

libdf's `df_create` panics across the C ABI (aborting the process) on an unreadable model. The
Go side checks the file exists and is non-empty first, but a corrupt archive would still
abort.
