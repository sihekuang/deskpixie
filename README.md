# DeskPixie

A desktop voice pixie: you talk with **Pixie**, and Pixie hands real work to **the coordinator**, a Claude Code agent (or OpenAI Codex: Settings ▸ Coordinator ▸ Coordinator agent) working in the background. A Go core (`core/`, built as the C-ABI dylib `libdeskpixie`) hosted by a Swift macOS app (`mac/`).

This repo is the port of [ai-mascot](https://github.com/sihekuang/ai-mascot) (Rust/Tauri). See `docs/specs/2026-10-13-deskpixie-port-design.md`.

## Supported Herdr versions

Delegation runs the coordinator (Claude Code) in a [Herdr](https://herdr.dev) session. DeskPixie supports
**Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)** (`compat.SupportedVersions` in
`core/herdr/compat`). The `herdr` program and the session's running server must be the same
version; otherwise the tray shows "Coordinator: herdr version mismatch: …" with the version you have.
After updating herdr, restart DeskPixie's session (`herdr session stop deskpixie`, which closes
its panes) so a new server starts. Herdr also warns when its Claude Code hook is outdated:
`herdr integration status` should say `claude: current`; DeskPixie never installs it for you.
