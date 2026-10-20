# Third-party notices

DeskPixie is licensed under the Apache License 2.0 (see [LICENSE](LICENSE)). It includes or
bundles the following third-party components, each under its own license.

| Component | Used for | License | Where |
|---|---|---|---|
| [coder/websocket](https://github.com/coder/websocket) v1.8.15 | Voice provider connections (Go) | ISC | compiled into libdeskpixie |
| [creack/pty](https://github.com/creack/pty) v1.1.24 | `pix`: running agents in a terminal (Go) | MIT | compiled into pix |
| [golang.org/x/term](https://pkg.go.dev/golang.org/x/term) v0.46.0, [golang.org/x/sys](https://pkg.go.dev/golang.org/x/sys) v0.48.0 | Terminal and system calls (Go) | BSD-3-Clause | compiled into libdeskpixie and pix |
| [DeepFilterNet](https://github.com/Rikorose/DeepFilterNet) v0.5.6: libdf and the DeepFilterNet3 model | Microphone noise filter | MIT OR Apache-2.0 | `core/audio/denoise/libdf/`, bundled as `libdf.dylib` and `DeepFilterNet3.tar.gz` |
| [Hush](https://github.com/pulp-vision/Hush) by weya AI (`hush16k.tar.gz`) | Default noise-filter model | Apache-2.0 | `core/audio/denoise/models/` ([LICENSE-hush](core/audio/denoise/models/LICENSE-hush)), embedded in libdeskpixie |
| [Chart.js](https://www.chartjs.org) 4.5.1 | Charts on canvas pages | MIT | `design-system/vendor/` ([LICENSES.md](design-system/vendor/LICENSES.md)) |
| [Mermaid](https://mermaid.js.org) 12.1.0 | Diagrams on canvas pages | MIT | `design-system/vendor/` ([LICENSES.md](design-system/vendor/LICENSES.md)) |

Hush's training data includes ESC-50 (CC BY-NC) and the DNS Challenge set (Microsoft research
license). Whether that limits weights released under Apache-2.0 is unsettled; see
[core/audio/denoise/models/README.md](core/audio/denoise/models/README.md) and #97.

DeskPixie talks to voice and agent services you set up yourself (Google Gemini Live, OpenAI
Realtime, Anthropic Claude Code, OpenAI Codex, herdr, tmux). None of them is bundled; their own
terms apply.

The license texts of the Go modules follow. DeepFilterNet's are in its repository; Hush's, Chart.js's
and Mermaid's are linked above.

## coder/websocket (ISC)

```
Copyright (c) 2025 Coder

Permission to use, copy, modify, and distribute this software for any
purpose with or without fee is hereby granted, provided that the above
copyright notice and this permission notice appear in all copies.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
```

## creack/pty (MIT)

```
Copyright (c) 2011 Keith Rarick

Permission is hereby granted, free of charge, to any person
obtaining a copy of this software and associated
documentation files (the "Software"), to deal in the
Software without restriction, including without limitation
the rights to use, copy, modify, merge, publish, distribute,
sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so,
subject to the following conditions:

The above copyright notice and this permission notice shall
be included in all copies or substantial portions of the
Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY
KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE
WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR
PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS
OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR
OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR
OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
```

## golang.org/x/term and golang.org/x/sys (BSD-3-Clause)

```
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```
