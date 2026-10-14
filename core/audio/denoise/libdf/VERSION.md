# libdf vendored binary

Copied from Howl's `core/third_party/deepfilter/` (`make -C core libdf`), which built it as below.

| Field | Value |
|---|---|
| Upstream | https://github.com/Rikorose/DeepFilterNet |
| Tag | v0.5.6 |
| Commit | 978576aa8400552a4ce9730838c635aa30db5e61 |
| Build target | aarch64-apple-darwin |
| MACOSX_DEPLOYMENT_TARGET | 13.0 |
| Rust version | rustc 1.95.0 (59807616e 2026-04-14) |
| Cargo package | deep_filter |
| Cargo features | capi |
| cbindgen version | 0.29.2 |
| Build date | 2026-04-29T18:18:37Z |
| Install name | `@rpath/libdf.dylib` |

| File | SHA-256 |
|---|---|
| `lib/libdf.dylib` | `3344989f46896a010ef7a157a6ef1ca8f84f3afa67e5a6623a4b37acbd5c0e10` |
| `models/DeepFilterNet3.tar.gz` | `c94d91f70911001c946e0fabb4aa9adc37045f45a03b56008cb0c8244cb63616` |

## Build notes

Two adjustments were required against an unmodified `v0.5.6` clone:

1. `cargo update -p time` to pull `time v0.3.28 -> v0.3.44`. The pinned version in the upstream
   `Cargo.lock` does not compile on rustc 1.95.
2. Added `crate-type = ["cdylib", "rlib"]` to `libDF/Cargo.toml`'s `[lib]` section so a `.dylib`
   is emitted. Upstream relies on `cargo-c` to produce the shared library; this bypasses that and
   uses plain `cargo build` plus `cbindgen` for the header.

The C header was generated separately with
`cbindgen --config cbindgen.toml --crate deep_filter --output deep_filter.h` (cbindgen 0.29.2).

The model is upstream's `models/DeepFilterNet3_onnx.tar.gz`, renamed. See README.md for the
full from-source steps.
