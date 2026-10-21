# Attachments Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Paste, drop, pick or (by voice) take from the clipboard an image or file, and hand it to the coordinator and its workers with the next request.

**Architecture:** Go owns one store of waiting attachments (files under the coordinator's workspace) and puts `Attached: <path>` lines into the next request or answer; when a worker is prompted, files it names are copied into the worker's folder. Swift owns the clipboard (behind a protocol), the text box chips, the hint row, Pixie's "Want me to take this?" bubble, paste/drop/picker and image shrinking, and talks to Go through new `attachments.*` calls and an `attachments` event.

**Tech Stack:** Go 1.2x (core), Swift 6 / SwiftUI / AppKit (mac), XCTest, `go test`.

**Spec:** `docs/superpowers/specs/2026-10-21-attachments-design.md`

## Global Constraints

- 2 MB per attachment (`MaxBytes = 2 << 20`), every kind; at most 5 waiting (`MaxItems = 5`).
- Over-size image: Swift shrinks it until ≤ 2 MB. Over-size other file: refused with "That file is over 2 MB."
- Images accepted as is: PNG, JPEG, GIF, WebP; TIFF/HEIC converted to PNG by Swift.
- Request line format, one per attachment, appended after the text: `Attached: <absolute path>`.
- Storage root: `<app data>/agent/workspace/attachments`; files 0600, folders 0700.
- Worker copies: `<worktree>/.deskpixie/attachments/`, with `<worktree>/.deskpixie/.gitignore` containing `*`.
- Detection (`NSPasteboard.detect…`) never reads; reading happens only on Cmd-V, the Attach row, the bubble, or the voice tool.
- Offered: file URLs; an image with no plain/rich text other than a URL. Never: text, RTF, HTML copies.
- Reply mode never changes because of attachments (#221).
- Tests never touch the real clipboard, home or app data (`t.TempDir()`, a fake pasteboard).

## Review Focus

1. Two attachments with the same file name (two "Screenshot.png") must both survive with distinct names → Task 1 `TestSameNameGetsADistinctFile`.
2. A request sent while delegation is off must not swallow the waiting attachments → Task 2 `TestAttachmentsWaitWhenDelegationIsOff`.
3. A worker re-prompted (nudge resend) must not get the files copied twice or paths rewritten twice → Task 3 `TestForWorkerIsIdempotent`.
4. A file name with control characters, slashes or `..` must not escape the attachments folder → Task 1 `TestNamesCannotEscape`.
5. The voice tool with an empty or text-only clipboard says so instead of attaching nothing silently → Task 4 `TestTakeClipboardWithNothingAttachable`.

---

### Task 1: The attachment store (Go)

**Files:**
- Create: `core/attach/store.go`, `core/attach/store_test.go`

**Interfaces:**
- Produces:
  ```go
  package attach
  const MaxBytes int64 = 2 << 20
  const MaxItems = 5
  type Item struct { ID, Name, Kind, Path string; Size int64 } // Kind: "image" | "file"
  func New(root string, now func() time.Time) *Store
  func (s *Store) OnChange(f func([]Item))           // called after every change, with Pending()
  func (s *Store) Add(name string, data []byte) (Item, error)   // bytes (clipboard, pasted image)
  func (s *Store) AddFile(path string) (Item, error)            // a dropped or picked file: copied in
  func (s *Store) Remove(id string) bool
  func (s *Store) Pending() []Item
  func (s *Store) Len() int
  func (s *Store) Take() []string   // moves pending to sent/<stamp>/, returns their absolute paths, empties the set
  func (s *Store) Sweep()           // at start: delete pending/; delete sent/* and recorded copies older than 14 days
  ```
- Layout: `root/pending/<id>/<name>`, `root/sent/<YYYYMMDD-HHMMSS>-<id>/<name>`, `root/copies.jsonl` (`{"path":…,"at":…}`).
- Kind is "image" by extension (.png .jpg .jpeg .gif .webp), else "file".

- [ ] **Step 1: Write the failing tests** in `store_test.go`:
  - `TestAddKeepsTheBytesPrivately`: Add("Screenshot 14.32.png", png) → Item.Kind "image", file content equal, mode 0600, parent dir 0700, OnChange got 1 item.
  - `TestTooBigIsRefused`: Add with MaxBytes+1 bytes → error text exactly "That file is over 2 MB."; Len()==0.
  - `TestAtMostFive`: 6th Add → error "You can attach up to 5 files."
  - `TestSameNameGetsADistinctFile`: two Add("a.png") → different Paths, both readable.
  - `TestNamesCannotEscape`: Add("../../x\n.png") and Add("/etc/passwd") → Path stays under root/pending; Name has no "/" or control chars.
  - `TestAddFileCopiesAndChecksSize`: AddFile(temp file 10 bytes) → copy exists, original untouched; AddFile(missing) → error "Couldn't read that file."; AddFile(dir) → same error.
  - `TestRemoveDeletesAtOnce`: Remove(id) → true, file gone, OnChange got 0 items; Remove("nope") → false.
  - `TestTakeMovesAndEmpties`: two Adds, Take() → 2 absolute paths under root/sent, files exist; Len()==0; second Take() → nil.
  - `TestSweep`: a pending file and a sent dir with mtime 15 days ago and one 1 day ago → after Sweep: pending gone, old sent gone, new sent kept.
- [ ] **Step 2:** `cd core && go test ./attach/` → FAIL (package missing).
- [ ] **Step 3:** Implement `core/attach/store.go` (mutex-guarded; ids from 8 random hex bytes; name cleaned with `filepath.Base`, control chars and `/` stripped, empty → "attachment").
- [ ] **Step 4:** `go test ./attach/` → PASS.
- [ ] **Step 5:** Commit `Attachments: the store, 2 MB and 5 at most (#240)`.

### Task 2: Requests and answers carry them (Go)

**Files:**
- Modify: `core/agent/runtime.go` (`Deps`, `delegate`, `AnswerAsked`), `core/agent/chat.go` (`ChatSend`)
- Create: `core/agent/attachments.go`, `core/agent/attachments_test.go`

**Interfaces:**
- Consumes: Task 1 `*attach.Store` methods `Len`, `Take`.
- Produces:
  ```go
  // in agent: the seam (attach.Store satisfies it)
  type Attachments interface { Len() int; Take() []string; ForWorker(text, dir string) string }
  // Deps gains: Attachments Attachments  // nil = none
  func attachedLines(paths []string) string // "\nAttached: p" per path, "" for none
  ```
- `delegate` (typed and voice) takes the attachments only after the disabled/quitting checks pass, appending `attachedLines` to the task. `AnswerAsked` appends them to the answer text the same way (and to the fallback Delegate). `ChatSend` accepts empty text when `Len() > 0`, using the task "See the attached files."

- [ ] **Step 1: Failing tests** in `attachments_test.go` (with a fake `Attachments` recording calls):
  - `TestATypedRequestCarriesItsAttachments`: fake with 2 paths; ChatSend("what's wrong?") → the coordinator prompt (via the existing runtime rig used by chat tests) contains "what's wrong?\nAttached: /a/1.png\nAttached: /a/2.pdf".
  - `TestAVoiceRequestCarriesThemToo`: `Delegate("fix it","")` → prompt contains "Attached: /a/1.png".
  - `TestAnAnswerCarriesThem`: a typed job waiting on a question; ChatSend("yes") → the answer text the coordinator gets contains "Attached: /a/1.png".
  - `TestAttachmentsAloneAreARequest`: ChatSend("") with Len 1 → no error; prompt has "See the attached files." and the line.
  - `TestAttachmentsWaitWhenDelegationIsOff`: disabled controller; ChatSend("x") → error, fake.Take never called.
- [ ] **Step 2:** `go test ./agent -run 'Attach'` → FAIL.
- [ ] **Step 3:** Implement as in Interfaces.
- [ ] **Step 4:** `go test ./agent` → PASS (whole package, since chat tests share the path).
- [ ] **Step 5:** Commit `Attachments: the next request or answer carries them (#240)`.

### Task 3: Workers get copies (Go)

**Files:**
- Modify: `core/agent/controller.go` (Effect gains `Dir string`), `core/agent/controller_workers.go:362,487` (set `Dir: w.Worktree` on `EfPromptWorker`), `core/agent/runtime.go:1694` (rewrite text before `session.Prompt`)
- Modify: `core/attach/store.go` (`ForWorker`), `core/attach/store_test.go`
- Modify: `core/app/agentapp/assets/repo-work-workers.md` (one paragraph, below)

**Interfaces:**
- Produces: `func (s *Store) ForWorker(text, dir string) string`: for each `Attached: <path>` line whose path is under `root/sent`, copy the file to `dir/.deskpixie/attachments/<name>` (unless already there with the same size), write `dir/.deskpixie/.gitignore` = `*\n`, record the copy in `copies.jsonl`, and replace the path in the line. Lines with other paths, and `dir == ""`, are left as they are.
- Instruction paragraph (exact): "When a request has `Attached:` lines, pass those lines on unchanged in the task or message you give a worker or one of the user's sessions. DeskPixie copies the files into a worker's folder for it; a session gets the paths as they are and may ask the user to allow reading them."

- [ ] **Step 1: Failing tests:**
  - attach: `TestForWorkerCopiesAndRewrites` (text with one sent path → returned text names `dir/.deskpixie/attachments/x.png`, file exists, .gitignore is "*\n", copies.jsonl has the path); `TestForWorkerIsIdempotent` (calling twice on the returned text → unchanged, one copy, one record); `TestForWorkerLeavesOtherPaths` ("Attached: /etc/hosts" unchanged, nothing copied); `TestSweepRemovesOldCopies` (a recorded copy 15 days old is deleted).
  - agent: `TestAWorkerPromptGetsLocalPaths` (runtime rig with a fake Attachments whose ForWorker records (text, dir); a started worker's first prompt → ForWorker called with the worker's worktree, and the host got the rewritten text).
  - agentapp: update `files_test.go`'s pinned instructions for the new paragraph, and the prompt hash.
- [ ] **Step 2:** run `go test ./attach ./agent ./app/agentapp` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** same command → PASS.
- [ ] **Step 5:** Commit `Attachments: workers get copies in their folder; sessions get the paths (#240)`.

### Task 4: Calls, event and the voice tool (Go, app layer)

**Files:**
- Modify: `core/app/agentapp/agentapp.go` (build the store at `<workspace>/attachments`, `Sweep()` at start, pass it in `agent.Deps.Attachments`, expose `func (a *Agent) Attachments() *attach.Store`), `core/app/agent.go` (interface gains `Attachments() *attach.Store`)
- Create: `core/app/attachments.go`, `core/app/attachments_test.go`

**Interfaces:**
- Calls (registered in `registerAttachments(r)`, called next to `registerChat`):
  - `attachments.add` `{name, data}` (data base64) or `{path}` → `{item}`; errors as Task 1's texts.
  - `attachments.remove` `{id}` → `{}`.
  - `attachments.list` → `{items}`.
  - `clipboard.done` `{request, error?}` → `{}` (the shell's reply to a read request).
- Event `attachments` `{items}` on every change (store `OnChange`).
- Event `clipboard.read` `{request}`: Go asks the shell to read the clipboard and add what it finds through `attachments.add`, then call `clipboard.done`.
- Voice tool `take_clipboard` (no parameters), registered with the voice tools only when the agent exists. Description: "Attach what the user has copied (a screenshot, an image or files) to their next request to the coordinator. Use when they ask you to take, look at or use what they copied. Then call delegate with their request." Invoke: emit `clipboard.read`, wait up to 10 s for `clipboard.done` with that request id; reply `{"attached": [names]}` or `{"error": text}`; the texts:
  - nothing attachable: "There's no image or file on the clipboard."
  - denied: "DeskPixie isn't allowed to read the clipboard. Allow it in System Settings, Privacy & Security, Paste from Other Apps."
  - no reply in 10 s: "Couldn't read the clipboard."

- [ ] **Step 1: Failing tests** in `attachments_test.go` (app harness, `t.TempDir()` app data):
  - `TestAddListRemove`: add `{name:"a.png",data:<base64 png>}` → item with Kind "image"; `attachments` event emitted with 1 item; list → 1; remove → event with 0.
  - `TestAddRefusesTooBig`: 2 MB + 1 → the error text.
  - `TestTakeClipboard`: invoke the tool in a goroutine; the harness sees `clipboard.read` with a request id, calls `attachments.add` then `clipboard.done {request}` → tool reply lists "a.png".
  - `TestTakeClipboardWithNothingAttachable`: `clipboard.done {request, error:"There's no image or file on the clipboard."}` → tool reply carries that error.
  - `TestTakeClipboardTimesOut`: with the wait shortened through a package var, no reply → "Couldn't read the clipboard."
- [ ] **Step 2:** `go test ./app -run 'Attach|Clipboard'` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `cd core && gofmt -l . && go vet ./... && go test ./...` → all pass.
- [ ] **Step 5:** Commit `Attachments: calls, the attachments event and Pixie's take_clipboard tool (#240)`.

### Task 5: Clipboard behind a protocol, and what counts (Swift package)

**Files:**
- Create: `mac/Packages/DeskPixieCore/Sources/DeskPixieCore/Pixie/Clipboard.swift`, `Tests/DeskPixieCoreTests/ClipboardTests.swift`

**Interfaces:**
- Produces:
  ```swift
  public enum ClipboardKind: Equatable { case image, files(Int), none }
  public protocol ClipboardSource {               // NSPasteboard in the app; a fake in tests
      var changeCount: Int { get }
      func detectedTypes() -> Set<String>          // UTIs from detect, never a read
      func readImagePNG() -> Data?                 // a read (may prompt)
      func readFileURLs() -> [URL]                 // a read (may prompt)
  }
  public enum ClipboardRules {
      /// Offered: file URLs; an image with no text besides a URL. Never text, RTF or HTML.
      public static func kind(_ types: Set<String>) -> ClipboardKind
      public static func hint(_ k: ClipboardKind) -> String?  // "Screenshot copied", "1 file copied", "2 files copied"
  }
  ```
  (`files(n)`: the count isn't knowable without a read; `detectedTypes` returns "public.file-url" once, so the hint says "File copied" for files. Keep `files(Int)` with 1.)
- [ ] **Step 1: Failing tests:** `kind` for: `["public.png"]` → .image; `["public.tiff","public.url"]` → .image; `["public.file-url"]` → .files(1); `["public.utf8-plain-text"]` → .none; `["public.rtf","public.png"]` → .none; `["public.html","public.png","public.utf8-plain-text"]` → .none; `[]` → .none. `hint(.image)` == "Screenshot copied", `hint(.files(1))` == "File copied", `hint(.none)` == nil.
- [ ] **Step 2:** `swift test --filter ClipboardTests` → FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit `Attachments: what counts on the clipboard, behind a protocol (#240)`.

### Task 6: The attachment set model and image shrinking (Swift package)

**Files:**
- Create: `Sources/DeskPixieCore/Pixie/AttachmentModel.swift`, `Sources/DeskPixieCore/Pixie/ImageShrink.swift`, tests `AttachmentModelTests.swift`, `ImageShrinkTests.swift`
- Modify: `Sources/DeskPixieCore/Bridge/EngineEvent.swift` (cases `.attachments([AttachmentItem])`, `.clipboardRead(request: String)`)

**Interfaces:**
- Produces:
  ```swift
  public struct AttachmentItem: Decodable, Equatable, Identifiable { public let id, name, kind, path: String; public let size: Int }
  public struct AttachmentModel: Equatable {
      public private(set) var items: [AttachmentItem]
      public var notice: String?                 // last refusal text, shown on the hint row
      public mutating func apply(_ items: [AttachmentItem])
      public var chipsVisible: Bool { !items.isEmpty }
  }
  public enum ImageShrink {
      /// PNG data ≤ maxBytes: the image as PNG if it fits, else scaled down (then JPEG) step by step.
      public static func fit(_ data: Data, maxBytes: Int) -> Data?
  }
  ```
- [ ] **Step 1: Failing tests:** event decoding of `{"kind":"attachments","items":[…]}` and `{"kind":"clipboard.read","request":"r1"}`; `apply` replaces items; `ImageShrink.fit` of a generated 3000×3000 noise PNG with maxBytes 2 MB → non-nil, ≤ 2 MB, decodes as an image; a 10×10 PNG → returned unchanged in size class (≤ original + small).
- [ ] **Step 2–4:** FAIL, implement (CGImage/NSBitmapImageRep), PASS.
- [ ] **Step 5:** Commit `Attachments: the set model, events and image shrinking (#240)`.

### Task 7: Chips, hint row, 📎, paste and drop in the box (Swift app)

**Files:**
- Modify: `mac/DeskPixie/Pixie/QuickChatView.swift` (chips row above the input; 📎 button at the input's start; hint row; `onPasteCommand`, `onDrop`), `mac/DeskPixie/Pixie/AgentRosterController.swift` (owns `AttachmentModel`, the `ClipboardSource` (NSPasteboard adapter), add/remove/send calls; `send()` sends with empty text when chips exist; card shows chips without input in voice mode), `mac/DeskPixie/Engine/EngineCoordinator.swift` (route `.attachments`, `.clipboardRead`)
- Create: `mac/DeskPixie/Pixie/PasteboardClipboard.swift` (the NSPasteboard `ClipboardSource`, using `detect` for `detectedTypes`)
- Modify: `Sources/DeskPixieCore/Pixie/QuickChatModel.swift` (`canSend` true with attachments and no text) + its tests

**Behavior:** chip = thumbnail (images: `NSImage(contentsOfFile: path)`, 22×16) or file icon (`NSWorkspace.shared.icon(forFile:)`), name, ✕ → `attachments.remove`. Hint row shows `ClipboardRules.hint` while the box is open and the clipboard's kind is offered, with "Attach" → read (image: `ImageShrink.fit` then `attachments.add {name:"Screenshot HH.mm.png", data}`; files: `attachments.add {path}` each); refusal text from the call shows on the row. Cmd-V: if the clipboard has text, the default paste; else attach as the Attach button does. Drop: file URLs → `attachments.add {path}`; images → as Attach. 📎 → `NSOpenPanel` (multiple). `.clipboardRead(request)`: read as Attach does, then `clipboard.done {request, error?}` with the Task 4 texts.
- [ ] **Step 1:** Failing test in `QuickChatModelTests`: `canSend` with empty draft and attachments → true; with empty draft and none → false.
- [ ] **Step 2–4:** FAIL, implement, `swift test` PASS, `make -C mac build` (signed) succeeds.
- [ ] **Step 5:** Commit `Attachments: chips, the hint row, 📎, paste and drop in the text box (#240)`.

### Task 8: Pixie's bubble and dropping onto Pixie (Swift app)

**Files:**
- Create: `Sources/DeskPixieCore/Pixie/ClipboardWatch.swift` + `ClipboardWatchTests.swift` (pure: given successive `changeCount`s and kinds, says when to show the bubble)
- Create: `mac/DeskPixie/Pixie/CopyBubbleView.swift`; Modify: the Pixie panel/controller that places side bubbles (next to the updates bubble), and Pixie's view for `onDrop`

**Interfaces:**
- `public struct ClipboardWatch { public mutating func poll(changeCount: Int, kind: ClipboardKind) -> Bool }`: true once per new `changeCount` whose kind isn't `.none`; never on the first poll (what was copied before launch).
- Polling every 0.5 s on the main run loop (NSPasteboard has no change notification).
- Bubble: "Want me to take this?" + image or file icon, beside Pixie, fades after 5 s; click → read and add as Attach does (no mode change); hover keeps it until the pointer leaves.
- Drop onto Pixie → add as in Task 7.
- [ ] **Step 1:** Failing tests: first poll → false; new count with .image → true; same count again → false; new count with .none → false; new count with .files(1) → true.
- [ ] **Step 2–4:** FAIL, implement, PASS, signed build succeeds.
- [ ] **Step 5:** Commit `Attachments: Pixie offers what you copied, and takes a drop (#240)`.

### Task 9: End to end, review, PR

- [ ] Full suite: `(cd core && gofmt -l . && go vet ./... && go test ./...)`, `(cd mac/Packages/DeskPixieCore && swift test)`, signed `make -C mac build`.
- [ ] Hands-on on an isolated build (settings with own `agent.session`/`agent.port` written before the first launch): copy a generated PNG to the clipboard with a helper → the bubble appears; click it → chip appears (voice mode: in the card); text mode: paste with Cmd-V, Attach row, 📎, drop; a 3 MB PNG shrinks; a 3 MB PDF is refused with the text; send → the coordinator's pane (herdr test session) shows `Attached: …` and the file exists. Screenshots of each.
- [ ] Second-agent review of the branch; fix findings with failing tests first.
- [ ] Push `attachments`, PR closing #240 (what changed, how tested, what wasn't checked by hand), watch CI.
