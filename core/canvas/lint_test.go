package canvas

import (
	"strings"
	"testing"
)

// The spec section 4 skeleton with extra head and body content.
func lintPage(head, body string) string {
	return `<!doctype html><html><head><meta charset="utf-8"><title>T</title>` +
		`<link rel="stylesheet" href="/ds/tokens.css"><link rel="stylesheet" href="/ds/base.css">` +
		`<script src="/ds/vendor/chart.umd.min.js"></script><script src="/ds/vendor/mermaid.min.js"></script>` +
		`<script src="/ds/canvas.js"></script>` + head + `</head><body><main class="page">` + body + `</main></body></html>`
}

func rules(html string) []Rule {
	out := []Rule{}
	for _, w := range Lint(html) {
		out = append(out, w.Rule)
	}
	return out
}

func repeatRule(r Rule, n int) []Rule {
	out := []Rule{}
	for i := 0; i < n; i++ {
		out = append(out, r)
	}
	return out
}

func TestCleanSkeletonHasNoWarnings(t *testing.T) {
	eq(t, len(Lint(lintPage("", "<h1>Hi</h1>"))), 0)
}

func TestExternalUrlsInAttributesAreFlagged(t *testing.T) {
	html := lintPage(`<link rel="stylesheet" href="//cdn.example.com/x.css">`,
		`<img src='https://example.com/a.png'><a href=http://example.com>x</a><img srcset="a.png 1x, https://e.com/b.png 2x">`)
	eq(t, rules(html), repeatRule(RuleExternalURL, 4))
}

func TestExternalCssUrlsAreFlagged(t *testing.T) {
	html := lintPage(`<style>@import "https://fonts.example.com/f.css"; body { background: url('https://e.com/bg.png') }</style>`,
		`<div style="background:url(http://e.com/x.png)"></div>`)
	eq(t, rules(html), repeatRule(RuleExternalURL, 3))
}

func TestDataUrlsAndDsAssetsAreFine(t *testing.T) {
	html := lintPage(`<style>.x { background: url(data:image/png;base64,AAAA) } .y { background: url("/ds/x.svg") }</style>`,
		`<img src="data:image/svg+xml;base64,PHN2Zz4="><a href="#details">jump</a>`)
	eq(t, len(Lint(html)), 0)
}

func TestForeignScriptsAreFlagged(t *testing.T) {
	html := lintPage(`<script src="/js/app.js"></script><script src="https://cdn.example.com/x.js"></script>`, "")
	eq(t, rules(html), []Rule{RuleForeignScript, RuleForeignScript, RuleExternalURL})
}

func TestNetworkApisInInlineScriptsAreFlagged(t *testing.T) {
	html := lintPage("", `<script>fetch('/x'); const x = new XMLHttpRequest(); const w = new WebSocket('wss://e.com');</script>`)
	eq(t, rules(html), repeatRule(RuleNetworkAPI, 3))
	if !strings.Contains(Lint(html)[0].Detail, "fetch") {
		t.Error(Lint(html)[0])
	}
}

func TestWebrtcMetaRefreshAndWordBoundaries(t *testing.T) {
	html := lintPage(`<meta http-equiv="Refresh" content="0;url=x">`, `<script>new RTCPeerConnection(); fetch ('/x')</script>`)
	eq(t, rules(html), []Rule{RuleMetaRefresh, RuleNetworkAPI, RuleNetworkAPI})
	quiet := lintPage("", `<script>prefetch('/x'); myWebSocketLike(); const refetch = 1;</script>`)
	eq(t, len(Lint(quiet)), 0)
}

func TestImportUrlIsReportedOnce(t *testing.T) {
	eq(t, rules(lintPage(`<style>@import url("https://fonts.example.com/f.css");</style>`, "")), []Rule{RuleExternalURL})
}

func TestFormsFramesAndPageLinksAreFlagged(t *testing.T) {
	html := lintPage("", `<form><input></form><iframe></iframe><embed><a href="/pages/other.html">next</a>`)
	eq(t, rules(html), []Rule{RuleForm, RuleFrame, RuleFrame, RulePageLink})
}

func TestMissingTokensAndTitleAreFlagged(t *testing.T) {
	html := `<!doctype html><html><head><link rel="stylesheet" href="/ds/base.css"></head><body></body></html>`
	eq(t, rules(html), []Rule{RuleMissingTokens, RuleMissingTitle})
}

func TestMermaidTextCommentsAndStringsAreNotTags(t *testing.T) {
	html := lintPage("", "<pre class=\"mermaid\">flowchart LR\n  A --> B\n  B -.-> C\n  C <-- D</pre>"+
		`<!-- <img src="https://e.com/x.png"> -->`+
		`<script>const label = 'see https://example.com'; if (a < b) {}</script>`)
	eq(t, len(Lint(html)), 0)
}

func TestWarningsDisplayTheirRule(t *testing.T) {
	if got := Lint(lintPage("", "<iframe></iframe>"))[0].String(); got != "frame: <iframe> is blocked" {
		t.Error(got)
	}
}
