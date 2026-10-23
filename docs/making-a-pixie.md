# Making your own pixie

A pixie is the little character that sits on your desktop. DeskPixie comes with four (Bit,
Dino, Black Gold and Flash), and you can add your own without rebuilding the app: draw one
sprite sheet, write a short `pixie.json`, and put the two in your pixies folder.

## The quick way

1. Copy a built-in pixie as a starting point. They are in this repo under
   [`pixies/`](../pixies), and inside the app at
   `DeskPixie.app/Contents/Resources/pixies`. Dino has every animation, so it is the easiest
   to start from.
2. Rename the copy's folder to your pixie's id, for example `my-cat`. Use lowercase letters,
   numbers and `-`. The folder's name is the id; a folder named like a built-in pixie (`bit`)
   replaces that one.
3. Change `"name"` in `pixie.json` to what you want to see in the menu, for example
   `"My Cat"`.
4. Redraw `sheet.png` in your pixel editor (Aseprite, Pixelorama, Piskel, Photoshop…).
5. Put the folder in your pixies folder (below), then pick your pixie in **Settings ▸ Pixie**
   or the menu bar's **Pixie** menu.

## Your pixies folder

By default DeskPixie reads your pixies from:

```
~/Library/Application Support/com.sihekuang.deskpixie/pixies/
```

To keep them somewhere else, such as a folder you back up or keep in git, choose it in
**Settings ▸ Pixie ▸ Your pixies folder**. DeskPixie then reads that folder instead of the one
above. The built-in pixies are always there too. **Pixie ▸ Open Pixies Folder** in the menu bar
opens whichever folder is in use.

Each pixie is its own folder inside it:

```
my-pixies/
├── my-cat/
│   ├── pixie.json
│   └── sheet.png
└── my-owl/
    ├── pixie.json
    └── sheet.png
```

Folders whose names start with `.` (like `.git`) are skipped.

New and changed pixies show up when you choose the folder, when you open Settings, and when you
click **Pixie ▸ Reload Pixies** in the menu bar. You don't need to restart DeskPixie. If the
pixie you're drawing is the one on screen, Reload Pixies shows your latest sheet right away.

## The sprite sheet

Everything your pixie does comes from one PNG, a grid of equal cells:

- **One row per animation**, left to right, one cell per frame.
- **One extra row of mouth shapes**, used while Pixie talks.
- **Transparent background.** Use a PNG with alpha. Clear pixels are see-through, and clicks
  pass through them to the window below, so only your character can be dragged.
- **Any cell size.** The built-in pixies use 32×32 cells with 8 frames per row, so their sheets
  are 256 pixels wide. The sheet can be at most 4096×4096 pixels and 16 MB.

Dino's sheet (32×32 cells, 7 rows):

| Row | What it is |
|---|---|
| 0 | idle |
| 1 | listening |
| 2 | thinking |
| 3 | delegating |
| 4 | happy |
| 5 | error |
| 6 | mouth shapes |

Pixel art stays crisp: DeskPixie scales the sheet up by whole numbers (the **Size** setting)
with no smoothing, keeping Pixie's feet in place when the size changes.

## The animations

DeskPixie shows one animation at a time, by what Pixie is doing:

| State | When it shows | Needed? |
|---|---|---|
| `idle` | Resting, and while Pixie talks (the mouth row does the talking). | Yes |
| `listening` | Pixie is listening to you. | Yes |
| `thinking` | You've stopped talking and Pixie is working on an answer, or it's connecting. | Yes |
| `delegating` | A session is working on something Pixie handed off. | No: uses `thinking` |
| `happy` | A handed-off task is done, or a canvas page opened. | No: uses `idle` |
| `error` | Something went wrong: a failed task, or voice couldn't start. | No: uses `idle` |

Most animations repeat for as long as Pixie is in that state. `happy` and `error` are short:
give them `"loop": false` (as the built-in pixies do) and they play once, then Pixie goes back
to what it was doing. If they loop, they repeat for about 2 seconds instead.

## The mouth

A mouth is always drawn on top of the body, in every animation. While Pixie talks it opens
wider as the voice gets louder:

- The mouth row holds `levels` shapes, one per cell from the left. The first is the closed
  mouth, shown whenever Pixie is quiet. The last is the widest open.
- Only the top-left `size` pixels of each mouth cell are used.
- `anchor` is where the mouth's top-left corner goes inside the body cell, in pixels from the
  cell's top-left.
- If the face moves in a frame, say the head lifts in `happy`, give that state a `faceDy` list
  so the mouth moves with it: one number per frame, in pixels, negative is up.

Tip: draw the body frames without a mouth, so the mouth row is the only mouth you ever see.

## pixie.json

Dino's, in full:

```json
{
  "name": "Dino",
  "contract": 1,
  "cell": [32, 32],
  "sheet": "sheet.png",
  "fps": 8,
  "bob": { "px": 1, "frames": 4 },
  "states": {
    "idle":       { "row": 0, "frames": 8 },
    "listening":  { "row": 1, "frames": 8 },
    "thinking":   { "row": 2, "frames": 8 },
    "delegating": { "row": 3, "frames": 8 },
    "happy":      { "row": 4, "frames": 8, "loop": false, "bob": false,
                    "faceDy": [0, -1, -2, -3, -2, -1, 0, 0] },
    "error":      { "row": 5, "frames": 8 }
  },
  "mouth": { "row": 6, "anchor": [22, 14], "size": [7, 4], "levels": 4 }
}
```

| Field | Needed | What it is |
|---|---|---|
| `name` | Yes | What the menu and Settings call your pixie. |
| `contract` | Yes | Always `1`. |
| `cell` | Yes | `[width, height]` of one cell, in pixels. |
| `sheet` | Yes | The PNG's file name, inside the pixie's folder. |
| `fps` | Yes | Frames per second, 1 to 60. |
| `bob` | No | A gentle up-and-down: the body sits `px` pixels lower (at most 16) for `frames` frames, then back up for `frames` frames. Leave it out for none. |
| `states` | Yes | The animations (above). Each has `row` (from 0, top) and `frames`. |
| `states.*.loop` | No | `false` plays once, then Pixie goes back to what it was doing. Default `true`. |
| `states.*.bob` | No | `false` turns the bob off for this animation. Default `true`. |
| `states.*.faceDy` | No | Moves the mouth up or down per frame: one number per frame. |
| `mouth` | Yes | `row`, `anchor` `[x, y]`, `size` `[width, height]`, and `levels` (2 or more). |

Other fields are ignored, so you can keep notes in the file, for example `"author"`.

## When a pixie doesn't load

A pixie that can't be read doesn't show up in the list. The menu bar's **Pixie** menu says
which one and why (the first five, then how many more), for example:

```
my-owl didn't load: mouth.row: row 9 exceeds sheet rows (7)
```

The part after `didn't load:` names the field and the problem:

| Message | Fix |
|---|---|
| `pixie.json: cannot read: not found` | The folder has no `pixie.json`. Check the name, or move other folders out. |
| `pixie.json: missing field(s): …` | Add the fields it lists. |
| `pixie.json: invalid character …` | The JSON has a typo, often a missing or extra comma. |
| `sheet: sheet.png not found` | The `sheet` name doesn't match the PNG's file name. |
| `sheet: sheet.png is not a PNG` | Save the sheet as a PNG. |
| `states.idle: required state missing` | Add `idle`, `listening` and `thinking`. |
| `….row: row N exceeds sheet rows (M)` | The sheet has only M rows of `cell` height: check `row`, `cell` or the sheet's height. |
| `….frames: N frames need Wpx width, sheet is Spx` | The sheet isn't wide enough for that many frames. |
| `mouth.anchor: anchor + size … outside cell` | The mouth must fit inside one cell: check `anchor` and `size`. |
| `….faceDy: has N entries, expected M` | Give one number per frame. |
| `contract: unsupported contract` | Set `"contract": 1`. |

Fix the file and click **Pixie ▸ Reload Pixies**.

## Sharing your pixie

A pixie is just its folder: zip it and send it, and the other person puts it in their pixies
folder. If you'd like yours to ship with DeskPixie, open a pull request that adds it under
[`pixies/`](../pixies). Only share art you made or have the right to share.
