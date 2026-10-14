# Rename "mascot" to "pixie" — #68

Status: approved by the owner 2026-10-14 (scope: everything, with migration).

## Rule
Everywhere the product, code or contracts say "mascot", say "pixie" instead. "Bit" (the
character's name) is unchanged. Historical docs (past specs, decisions, parity results) are not
rewritten; user-facing docs (README, setup) are.

## Contracts and back-compat
| Contract | New | Back-compat |
|---|---|---|
| Worker branches | `pixie/<slug>` | Existing `mascot/<slug>` worktrees are still recognized, reused, audited and cleaned up (branch/lock/log checks accept both prefixes); new workers get `pixie/`. |
| MCP server name | `pixie` (`mcp__pixie__*`) | Running/kept agents launched with the old config keep working: the hook server authenticates by token, not name; code matching tool names (`narrate.go`, permission allow-list checks) accepts both prefixes; Codex approval marker matches "Allow the pixie MCP server" and the old "mascot" text. |
| Agent token env | `PIXIE_AGENT_TOKEN` | Both names scrubbed from child/server envs; kept agents keep their old variable (token value is what matters). |
| Pack manifest | `pixie.json` | `mascot.json` read when `pixie.json` is absent. |
| Pack folders | `pixies/` (bundle Resources and Application Support) | First launch renames Application Support `mascots/` → `pixies/` when `pixies/` doesn't exist (idempotent); if both exist both are read. Bundle folder renamed at build. EngineConfig field `bundledMascots` → `bundledPixies`. |
| Engine calls | `pixie.click`, `pixie.moved` | Core and shell ship together; no alias. |
| Settings / tray | Section "Pixie", pack field "Pixie", tray submenu "Pixie" | No settings.json keys change. Swift section enum raw value follows the core's ("Pixie"). |

## Code
Identifiers, files, folders and tests renamed (`MascotController` → `PixieController`,
`MascotPanel`, `MascotView`, `SectionMascot`, `mac/DeskPixie/Mascot/`,
`DeskPixieCore/Mascot/`, `MascotSettings`, etc.). xcodegen picks up moved files.

## Testing
Unit tests per back-compat path (old branch prefix reuse/cleanup, old manifest, old folder,
old MCP tool name, old approval marker, env scrub of both names); folder migration idempotent;
full Go/Swift suites; `make build`; live smokes on Herdr and tmux with `SMOKE_CODEX=1`.
