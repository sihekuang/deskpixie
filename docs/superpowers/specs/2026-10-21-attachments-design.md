# Attachments: paste, drop or hand Pixie a file (#240)

## Why

You often want Pixie's agents to look at something you have in front of you: a screenshot of an
error, a PDF, a log file. Today the only way is to save it somewhere and type its path. DeskPixie
should take the attachment the way you'd hand it to a person: paste it, drop it, or say "take what
I copied", and the coordinator (and the worker it hands the job to) gets the file with the request.

## Goals

- Notice when you've copied an image or a file, and offer it, without macOS's clipboard prompt.
- Five ways to attach, all landing in the same place (the text box's attachment chips):
  1. Cmd-V in the text box.
  2. "Attach" on the hint row in the text box.
  3. Drag and drop onto the text box, or onto Pixie (opens the box with the files attached).
  4. The 📎 button in the text box (a file picker).
  5. By voice: "take what I copied", or "yes" to Pixie's bubble.
- The coordinator gets each attachment as a file it can read, named in the request; a worker the
  coordinator hands the job to can read it too.

## Non-goals

- Pixie (the voice model) looking at the image itself. It only hands the file on.
- Copied text as an attachment. Text pastes into the box as text, as today; copying text never
  shows a hint or a bubble.
- Clipboard history, or attaching something copied earlier than the current clipboard.
- Windows/Linux shells (the Go side is written to be reused; the UI is Mac only for now).

## Anti-goals

- A macOS "paste from other apps" prompt for anything except an explicit request to take the
  clipboard (the bubble, the Attach row, or by voice). Noticing never reads.
- Losing an attachment silently: if one can't be added or delivered, you're told in plain words.
- Attachments readable by other users on the Mac, or kept forever.

## What you see

### Noticing a copy

When the clipboard changes to an image (including a screenshot copied with ⌃⇧⌘4) or one or more
files copied in Finder, DeskPixie checks only *what kind* of thing it is (NSPasteboard's `detect`
methods, which never trigger the prompt) and shows, every time:

- **Pixie's bubble:** a short bubble beside Pixie, "Want me to take this?" with a small thumbnail
  placeholder (an image icon or a file icon; the real thumbnail would need a read). It fades after
  about 5 seconds.
  - Clicking it opens the text box with the item attached.
  - In an open voice conversation, "yes" / "take it" attaches it to your next request.
  - Ignoring it attaches nothing.
- **The hint row:** while the text box is open, a row above the input says "Screenshot copied ·
  Attach" (or "2 files copied · Attach"). It stays until you attach, copy something else, or send.

Copying text shows neither. What counts (my call, from what `detect` can see without reading):
- **Offered:** a Finder file copy (file URLs); an image with no rich text (a screenshot; an image
  copied from a browser, which also carries its URL).
- **Not offered:** plain or rich text (RTF/HTML), even when it carries an image too (copying from
  a web page, Word or Excel): that's a text copy.
- **Cmd-V in the box:** pastes text when the clipboard has text, and attaches when it holds only
  an image or files.

### One set of waiting attachments (my call)

There is one set of attachments waiting for your next request, shared by text and voice mode and
owned by the core (Go). Every way to attach adds to it; the next request takes all of it,
whether you type it or say it. Until then the attachments wait (until you remove them, send, or
quit); nothing changes your reply mode (#221's rule stands):

- **Text mode:** the set shows as chips in the text box.
- **Voice mode:** the set shows as the same chips in the card under Pixie (where the caption
  shows), each with its ✕, without an input line. Clicking the bubble or dropping onto Pixie adds
  to it; it doesn't switch to text mode or open the input.

### Attachments in the box

Each attachment is a **named chip** above the input: a small thumbnail (images) or a file-type icon,
its name ("Screenshot 14:32", "invoice.pdf"), and an ✕ to remove it. Several chips wrap onto
more lines. The 📎 button sits at the start of the input. A message can be sent with attachments
and no text.

### Limits

- **2 MB per attachment**, every kind of file.
- A screenshot or other image over 2 MB is shrunk (scaled down, re-encoded as PNG or JPEG) until it
  fits, and attached.
- Any other file over 2 MB isn't added; the hint row says "That file is over 2 MB."
- Images: PNG, JPEG, GIF, WebP (what Claude reads). Other image types (TIFF from the clipboard,
  HEIC) are converted to PNG. Any other file is attached as it is.
- At most 5 attachments per message (builder's call on the exact number is fine; say it in the box
  when reached).

### Answers carry them too (my call)

When the coordinator is waiting on your answer (a typed job's question, or a question Pixie
passes on), the waiting attachments go with the answer and are stored with that job; the
`Attached:` lines go into the answer the coordinator gets.

### Voice

Pixie gets a tool, "take what I copied": it reads the clipboard (the one place DeskPixie reads it
on its own, so macOS asks once the first time; after that the user can set DeskPixie to Allow in
System Settings › Privacy & Security › Paste from Other Apps), saves the attachment, and the next
request Pixie hands the coordinator carries it. If you said "take it, and fix this", the request
and the attachment go together. If the clipboard holds nothing attachable, Pixie says so. If macOS
denies the read, Pixie says DeskPixie isn't allowed to read the clipboard and where to allow it.

## How it works

- **Go decides, Swift does** (AGENTS.md). Swift owns the clipboard, the text box, drag and drop,
  the file picker and image shrinking (NSPasteboard, NSImage). Go owns everything else: where an
  attachment is stored, the 2 MB rule's outcome messages, naming, cleaning up, putting attachments
  into the request, and getting them to workers.
- **New `dp_call`s** (names are the builder's): one to add an attachment (Swift hands Go the bytes
  or a file path plus a display name; Go validates, stores, returns an id and the chip's name), one
  to remove it, and `chat.send` gains the attachment ids. The voice tool runs in Go and asks the
  shell to read the clipboard (a new event/call pair: Go asks, Swift reads and replies with the
  data or a reason it couldn't).
- **Storage:** `<app data>/agent/workspace/attachments/<job or draft id>/<name>`, files 0600,
  folders 0700. That is inside the coordinator's working folder, so Claude Code and Codex
  coordinators can read them without a permission prompt.
- **The request:** the text typed into the coordinator's pane ends with one line per attachment,
  `Attached: /absolute/path/name.png`, which Claude Code reads with its Read tool (images
  included). The existing typed-request path carries it (no length limit; tmux uses a bracketed
  paste for multiline text).
- **Your own sessions (my call):** when the coordinator passes a request to one of your `pix`
  sessions (`message_session`), the message carries the attachments' absolute paths. Nothing is
  copied into your own checkout. That session's Claude Code may ask to read a path outside its
  folder; you are at that terminal to allow it. The coordinator is told this in its instructions.
- **Workers:** when the coordinator starts or messages a worker for a job that has attachments,
  the app copies them into the worker's folder (`<worktree>/.deskpixie/attachments/`, ignored by
  git) and rewrites the paths in what the worker is told. Workers' Claude Code reads outside their
  folder may ask permission, so the copy is what makes this reliable.
- **Cleanup (my call):** a job's attachments, and every copy made for its workers (the app
  records where each copy went, with the job, so it survives a restart), are deleted when the job
  is dropped (the existing 14-day cleanup). Waiting attachments you remove are deleted at once;
  ones still waiting when the app quits are deleted at the next start.

## Constraints

- macOS 26+ pasteboard privacy: `detect…` only for noticing; `readObjects`/`data(forType:)` only
  after an explicit request. Cmd-V in our own text field is user-initiated and never prompts.
- The app isn't sandboxed (entitlements `app-sandbox = false`); writing under app data is fine.
- Tests never touch the real clipboard, home or app data: the pasteboard sits behind a protocol in
  the Swift package with a fake in tests; Go uses `t.TempDir()`.
- Text-mode (#221) behavior is unchanged when there are no attachments.

## Left to the builder

- Exact names of calls, types and events; the chip and hint layout details within the mockup's
  look (chips above the input, thumbnail + name + ✕).
- How shrinking picks its steps (scale vs. quality), as long as the result is ≤ 2 MB and readable.
- The maximum number of attachments per message (around 5).
- How the paths in a worker's instructions are rewritten (the MCP start/message call can carry the
  job's attachment list instead of rewriting text, if simpler).

## To check early (unknowns from the research)

1. ~~That Claude Code reads an image from a typed absolute path inside its working folder without
   asking.~~ Checked 2026-10-21: `claude -p` in auto mode, given "Attached: <cwd>/attachments/
   test.png", described the image correctly with no prompt.
2. ~~Whether Codex reads an image from a typed path.~~ Checked 2026-10-21: `codex exec -s
   workspace-write` with the same line described it correctly.
3. That `detect` really doesn't prompt on macOS 27, and that the first explicit read does, once.
