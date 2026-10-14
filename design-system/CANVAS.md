# Canvas authoring guide

The canvas is a window in the DeskPixie app that shows **one self-contained HTML page** you write. Use it to *show* things: charts, architecture diagrams, tables and comparisons. The app styles every page with a shipped design system, so you write structure and data, not CSS.

## 1. The contract

- **Write one file:** `~/Library/Application Support/com.sihekuang.deskpixie/canvas/pages/<slug>.html`
  - `<slug>` is 1–64 characters of `a-z`, `0-9` and `-`, for example `q3-revenue` or `db-options`. Any other file name is ignored.
  - The page opens (or reloads) within about a second. Rewriting the same file refreshes it in place, so you can iterate.
  - To avoid showing a half-written file, either write it in one go, or write `.<slug>.html.tmp` (a leading dot is ignored) and rename it to `<slug>.html`.
  - Keep it under **5 MB**. Larger pages are not shown.
  - The app keeps the newest 50 pages and deletes older ones at startup.
- **The page is a static snapshot.** Inline all data. There is no network, no way to call back into the app and no navigation to other pages.
- **Name it with a `<title>`.** The title appears in the window title and in the tray's Recent list.

## 2. Skeleton

Start every page from this skeleton. Keep the five `/ds/` lines exactly as they are.

```html
<!doctype html><html><head><meta charset="utf-8"><title>Page title</title>
<link rel="stylesheet" href="/ds/tokens.css"><link rel="stylesheet" href="/ds/base.css">
<script src="/ds/vendor/chart.umd.min.js"></script><script src="/ds/vendor/mermaid.min.js"></script>
<script src="/ds/canvas.js"></script></head>
<body><main class="page">
  <header class="page-header"><h1>Page title</h1><p class="subtitle">One line of context</p></header>
  <!-- content -->
</main></body></html>
```

Put chart scripts at the **end of `<body>`**, after the elements they draw into.

## 3. Allowed and not allowed

| Allowed | Not allowed (blocked, and flagged by the linter) |
|---|---|
| Inline `<script>` and `<style>` | Any `http:`/`https:`/`//` URL in `src`, `href` or CSS `url()` |
| `data:` images and fonts | `fetch`, `XMLHttpRequest`, `WebSocket`, `EventSource` |
| Everything under `/ds/` | `<form>`, `<iframe>`, `<object>`, `<embed>` |
| Chart.js (`Canvas.chart`) and Mermaid (`<pre class="mermaid">`) | Links to `/pages/…` (one page at a time) |
| Links to `#anchors` within the page | Your own `<script src>` outside `/ds/` |
|  | `location` changes, `window.open`, `javascript:` and `mailto:` links, `<meta http-equiv="refresh">`, `<base>` |

Navigation is blocked, so any of the last row simply breaks the page. Lint warnings are logged with the page name; the page is still shown, but whatever was blocked will be missing.

## 4. Design tokens

Use tokens through `var(--name)` in any inline style. Never hard-code colors: tokens switch automatically between light and dark mode.

| Group | Tokens |
|---|---|
| Surfaces | `--c-bg` page, `--c-surface` cards, `--c-surface-2` table headers and code, `--c-border` |
| Text | `--c-text`, `--c-text-muted` |
| Accent and status | `--c-accent`, `--c-good`, `--c-warn`, `--c-bad`, each with a `-soft` background partner (`--c-good-soft` …) |
| Chart palette | `--chart-1` … `--chart-6`, in order (blue, orange, green, violet, pink, teal) |
| Type | `--font-sans`, `--font-mono`, sizes `--fs-xs` 12 · `--fs-sm` 13 · `--fs-md` 15 · `--fs-lg` 18 · `--fs-xl` 24 · `--fs-2xl` 32 |
| Space | `--sp-1` 4 · `--sp-2` 8 · `--sp-3` 12 · `--sp-4` 16 · `--sp-5` 24 · `--sp-6` 32 · `--sp-7` 48 |
| Shape | `--r-sm` 6 · `--r-md` 10 · `--r-lg` 14, `--shadow`, `--size-chart` 280px |

## 5. Components

**Page and header**
```html
<main class="page">
  <header class="page-header"><h1>Title</h1><p class="subtitle">Context line</p></header>
</main>
```

**Grid:** `.grid` auto-fits columns of at least 220 px; `.grid-2`, `.grid-3` and `.grid-4` fix the column count (one column on narrow windows). `.stack` spaces its children vertically.
```html
<div class="grid grid-3"> <div class="card">…</div> <div class="card">…</div> <div class="card">…</div> </div>
```

**Card**
```html
<section class="card"><h3 class="card-title">Section name</h3> … </section>
```

**Stat** (a KPI tile). `.stat-delta.up` is green and `.stat-delta.down` is red; they mean good and bad, not the arrow direction. A plain `.stat-delta` is neutral (grey), for "no change".
```html
<div class="card stat">
  <span class="stat-label">Weekly active users</span>
  <span class="stat-value">48,210</span>
  <span class="stat-delta up">▲ 6.2%</span>
</div>
```

**Callout**: `.callout-info`, `.callout-good`, `.callout-warn`, `.callout-bad`. Lead with the conclusion.
```html
<div class="callout callout-good"><p class="callout-title">Recommendation: SQLite</p><p>Why, in one or two sentences.</p></div>
```

**Badge**: `.badge-info`, `.badge-good`, `.badge-warn`, `.badge-bad`, or plain `.badge`.
```html
<span class="badge badge-warn">slow</span>
```

**Table**: plain `<table>` with `<thead>`. Add `class="num"` to numeric cells so they right-align. Inside a `.card` the table runs edge to edge.
```html
<table><thead><tr><th>Page</th><th class="num">Views</th></tr></thead>
<tbody><tr><td>Home</td><td class="num">182,400</td></tr></tbody></table>
```

**Legend** (for a chart with its legend turned off, or to explain a diagram). Swatches take the chart palette in order.
```html
<ul class="legend"><li class="legend-item"><span class="swatch"></span>Week 40</li><li class="legend-item"><span class="swatch"></span>Week 41</li></ul>
```

**Helpers:** `.muted` for secondary text, `.small` for 13 px text, `<code>` for identifiers.

## 6. Charts

Give the chart a sized container and call `Canvas.chart(selectorOrElement, config)` at the end of `<body>`. `config` is a normal Chart.js 4.5 config. `Canvas.chart` fills in palette colors, fonts, grid colors and tooltips from the tokens; any color you set yourself wins. Animations are off, because pages are snapshots.

```html
<section class="card"><h3 class="card-title">Requests by region</h3><div class="chart" id="regions"></div></section>
…
<script>
  Canvas.chart('#regions', {
    type: 'bar',
    data: { labels: ['US', 'EU', 'APAC'], datasets: [ { label: 'Week 40', data: [41, 22, 10] }, { label: 'Week 41', data: [42, 24, 11] } ] }
  });
</script>
```

- Types that work well: `line` (trends), `bar` (categories; `options: { indexAxis: 'y' }` for long labels), `radar` (scoring options on several criteria), `doughnut` (parts of a whole, at most 6 slices), `scatter`.
- `.chart` is 280 px tall. For another height use `style="height: 360px"` on the container.
- One idea per chart. Put the takeaway in the card title or a callout, not only in the chart.
- `Canvas.palette()` returns the six colors and `Canvas.token('--c-good')` any token, for when you need a specific color.

## 7. Diagrams (Mermaid 12)

Write Mermaid source inside `<pre class="mermaid">`. `canvas.js` renders every block with a theme built from the tokens. Flowcharts (`flowchart LR` or `TD`), sequence diagrams, state diagrams, class diagrams, ER diagrams, Gantt charts and timelines all work.

```html
<pre class="mermaid">
flowchart LR
  client[Client] --> api[API]
  api --> db[(Database)]
  api -- events --> queue{{Queue}}
</pre>
```

- Do not style nodes with hard-coded colors (`style A fill:#f00`); the theme follows light and dark mode.
- Arrows such as `-->` and `<--` are fine. Don't put raw `<tag`-like text or a bare `&` in diagram text: write `&lt;` and `&amp;`.
- Keep diagrams to about 15 nodes; split bigger systems into several diagrams with a heading each.
- Pair an architecture diagram with a component table (see `examples/architecture.html`).

## 8. Do and don't

**Do**
- Lead with the answer: a callout or stat row at the top, details below.
- Use one `.page-header`, then cards in grids. Keep a page to one topic.
- Use real numbers with units, and the `.num` class in tables.
- Use status colors only for meaning: good, warn and bad.

**Don't**
- Don't load anything from the internet: fonts, images, scripts and CSS frameworks are all blocked.
- Don't hard-code colors or fonts; use tokens.
- Don't build interactivity that needs the app: no forms, no buttons that do things.
- Don't write a giant page: if it needs a table of contents, split it into several pages.

## 9. Checklist before writing the file

1. The file name is `<slug>.html` with `[a-z0-9-]` only.
2. It has a `<title>` and the five `/ds/` lines from the skeleton.
3. There are no `http`/`https` URLs, no `fetch`, no forms and no iframes.
4. All data is inlined.
5. Each chart sits in a `.chart` container and `Canvas.chart` runs at the end of `<body>`.

## 10. Examples

The examples are in `examples/` next to this file (the app copies them there). Copy from these; they are known to pass the linter and to look right in light and dark mode.

- `examples/dashboard.html`: a stat row, a line chart, a bar chart, a callout and a table.
- `examples/architecture.html`: a Mermaid flowchart, a component table and callouts.
- `examples/comparison.html`: a recommendation callout, a radar chart, a bar chart and a comparison table with badges.
