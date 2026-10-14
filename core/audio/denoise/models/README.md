# Bundled noise-filter model

`hush16k.tar.gz` is **Hush** by weya AI, embedded into the app for the default noise model
(Settings ▸ Voice ▸ Noise model).

- **Source:** [pulp-vision/Hush](https://github.com/pulp-vision/Hush), `deployment/models/advanced_dfnet16k_model_best_onnx.tar.gz` at commit `7f4b5db565da584464eeb7f699b01723853845fa`. Weights are also on [weya-ai/hush](https://huggingface.co/weya-ai/hush).
- **SHA-256:** `45632ccaa82b71bb743d6caa7c78e983fe2f2790a3af7f6ec48e6ed7ba085df6`
- **Licence:** Apache-2.0 ([LICENSE-hush](LICENSE-hush)).
- **What it is:** DeepFilterNet3 retrained at 16 kHz with no lookahead (hop 160, FFT 320). It was trained with competing speakers 12–24 dB below the user, so it also turns background voices down. It is in the stock DeepFilterNet `.tar.gz` layout, so libdf (`df_create`) loads it as is. DeskPixie embeds it in libdeskpixie (`go:embed`, `deepfilter` build tag) and stages it to a temp file for libdf.
- **Caveat:** its training data includes ESC-50 (CC BY-NC) and the DNS Challenge set (Microsoft research licence). Whether that limits weights released under Apache-2.0 is unsettled; check before any commercial release (#97).

To update it, replace the file, then update the commit and checksum here (`shasum -a 256 hush16k.tar.gz`).
