package canvas

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Rule identifies a lint rule. The linter is a small tag scanner, not a full HTML parser: it
// understands comments, quoted attributes and the raw text of <script> and <style>, which is
// all the rules need. Warnings are logged with the page name; the page is still shown.
type Rule int

const (
	// RuleExternalURL: http(s):, //host or ws(s): in an attribute or a CSS url() / @import.
	RuleExternalURL Rule = iota
	// RuleForeignScript: <script src> not under /ds/.
	RuleForeignScript
	// RuleNetworkAPI: fetch(, XMLHttpRequest, WebSocket, EventSource, sendBeacon or
	// RTCPeerConnection in an inline script.
	RuleNetworkAPI
	// RuleMetaRefresh: <meta http-equiv="refresh"> would navigate the window away.
	RuleMetaRefresh
	// RuleForm: <form> cannot submit (CSP form-action 'none').
	RuleForm
	// RuleFrame: <iframe>, <frame>, <object> or <embed>.
	RuleFrame
	// RulePageLink: a link to /pages/...; v1 shows one page at a time.
	RulePageLink
	// RuleMissingTokens: no <link rel="stylesheet" href="/ds/tokens.css">.
	RuleMissingTokens
	// RuleMissingTitle: no non-empty <title>.
	RuleMissingTitle
)

func (r Rule) String() string {
	return [...]string{"external-url", "foreign-script", "network-api", "meta-refresh", "form", "frame", "page-link", "missing-tokens", "missing-title"}[r]
}

// Warning is one lint finding.
type Warning struct {
	Rule   Rule
	Detail string
}

func (w Warning) String() string { return fmt.Sprintf("%s: %s", w.Rule, w.Detail) }

var urlAttrs = []string{"src", "href", "action", "formaction", "poster", "data", "srcset", "background", "xlink:href"}

type networkAPI struct {
	name string
	call bool // must be followed by "("
}

var networkAPIs = []networkAPI{
	{"fetch", true},
	{"XMLHttpRequest", false},
	{"WebSocket", false},
	{"EventSource", false},
	{"sendBeacon", false},
	{"RTCPeerConnection", false},
	{"webkitRTCPeerConnection", false},
}

func isIdent(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$'
}

// usesAPI reports whether script uses api as a whole identifier (so prefetch( is not fetch().
func usesAPI(script string, api networkAPI) bool {
	from := 0
	for {
		i := strings.Index(script[from:], api.name)
		if i < 0 {
			return false
		}
		at := from + i
		from = at + 1
		before, _ := utf8.DecodeLastRuneInString(script[:at])
		boundary := at == 0 || !isIdent(before)
		after := script[at+len(api.name):]
		var tailOK bool
		if api.call {
			tailOK = strings.HasPrefix(strings.TrimLeftFunc(after, unicode.IsSpace), "(")
		} else {
			r, size := utf8.DecodeRuneInString(after)
			tailOK = size == 0 || !isIdent(r)
		}
		if boundary && tailOK {
			return true
		}
	}
}

// Lint returns warnings for things the CSP will block or that break the design-system contract.
func Lint(html string) []Warning {
	doc := scan(html)
	var out []Warning
	warn := func(rule Rule, detail string) { out = append(out, Warning{rule, detail}) }
	hasTokens := false

	for _, tag := range doc.tags {
		switch tag.name {
		case "form":
			warn(RuleForm, "<form> cannot submit; show data as a table instead")
		case "iframe", "frame", "object", "embed":
			warn(RuleFrame, fmt.Sprintf("<%s> is blocked", tag.name))
		}
		if v, ok := tag.attr("http-equiv"); tag.name == "meta" && ok && strings.EqualFold(strings.TrimSpace(v), "refresh") {
			warn(RuleMetaRefresh, `<meta http-equiv="refresh"> is blocked`)
		}
		if v, ok := tag.attr("href"); tag.name == "link" && ok && v == "/ds/tokens.css" {
			hasTokens = true
		}
		if tag.name == "script" {
			if src, ok := tag.attr("src"); ok && !strings.HasPrefix(strings.TrimSpace(src), "/ds/") {
				warn(RuleForeignScript, fmt.Sprintf(`<script src="%s"> is not a /ds/ asset`, src))
			}
		}
		for _, a := range tag.attrs {
			if contains(urlAttrs, a.name) {
				if isExternal(a.value) {
					warn(RuleExternalURL, fmt.Sprintf(`<%s %s="%s">`, tag.name, a.name, a.value))
				}
				if strings.Contains(a.value, "/pages/") {
					warn(RulePageLink, fmt.Sprintf(`<%s %s="%s"> links to another page`, tag.name, a.name, a.value))
				}
			}
			if a.name == "style" {
				for _, u := range cssURLs(a.value) {
					warn(RuleExternalURL, "style attribute loads "+u)
				}
			}
		}
	}
	for _, css := range doc.styles {
		for _, u := range cssURLs(css) {
			warn(RuleExternalURL, "<style> loads "+u)
		}
	}
	for _, script := range doc.scripts {
		for _, api := range networkAPIs {
			if usesAPI(script, api) {
				warn(RuleNetworkAPI, "inline script uses "+api.name)
			}
		}
	}
	if !hasTokens {
		warn(RuleMissingTokens, `add <link rel="stylesheet" href="/ds/tokens.css">`)
	}
	if _, ok := ExtractTitle(html); !ok {
		warn(RuleMissingTitle, "add a <title>; it names the page in the tray and window")
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasExternalPrefix(s string) bool {
	return strings.HasPrefix(s, "http:") || strings.HasPrefix(s, "https:") || strings.HasPrefix(s, "//")
}

func isExternal(value string) bool {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, p := range []string{"http:", "https:", "//", "ws:", "wss:", "ftp:"} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	for _, part := range strings.Split(v, ",") {
		if hasExternalPrefix(strings.TrimSpace(part)) {
			return true
		}
	}
	return false
}

// cssURLs returns external URLs loaded by CSS: url(...) and @import "...".
func cssURLs(css string) []string {
	lower := asciiLower(css)
	var found []string
	for _, n := range []struct {
		needle string
		stop   byte
	}{{"url(", ')'}, {"@import", ';'}} {
		from := 0
		for from <= len(lower) {
			i := strings.Index(lower[from:], n.needle)
			if i < 0 {
				break
			}
			start := from + i + len(n.needle)
			end := len(lower)
			if e := strings.IndexByte(lower[start:], n.stop); e >= 0 {
				end = start + e
			}
			raw := strings.TrimSpace(css[start:end])
			for strings.HasPrefix(raw, "url(") {
				raw = strings.TrimPrefix(raw, "url(")
			}
			raw = strings.Trim(raw, `"') `)
			if isExternal(raw) && !contains(found, raw) {
				found = append(found, raw)
			}
			from = end
		}
	}
	return found
}

type attr struct{ name, value string }

type tag struct {
	name  string
	attrs []attr
}

func (t *tag) attr(name string) (string, bool) {
	for _, a := range t.attrs {
		if a.name == name {
			return a.value, true
		}
	}
	return "", false
}

type document struct {
	tags    []tag
	scripts []string
	styles  []string
}

func isSpace(b byte) bool { return isASCIISpace(b) }

func scan(html string) document {
	lower := asciiLower(html)
	var doc document
	i := 0
	for {
		off := strings.IndexByte(lower[i:], '<')
		if off < 0 {
			return doc
		}
		i += off
		if strings.HasPrefix(lower[i:], "<!--") {
			if e := strings.Index(lower[i:], "-->"); e >= 0 {
				i += e + 3
			} else {
				i = len(html)
			}
			continue
		}
		next := byte(' ')
		if i+1 < len(html) {
			next = html[i+1]
		}
		if next == '/' || next == '!' || next == '?' {
			if e := strings.IndexByte(lower[i:], '>'); e >= 0 {
				i += e + 1
			} else {
				i = len(html)
			}
			continue
		}
		if !(next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z') {
			i++
			continue
		}
		t, end := parseTag(html, lower, i+1)
		i = end
		if t.name == "script" || t.name == "style" {
			bodyEnd := len(html)
			if e := strings.Index(lower[i:], "</"+t.name); e >= 0 {
				bodyEnd = i + e
			}
			body := html[i:bodyEnd]
			if t.name == "script" {
				doc.scripts = append(doc.scripts, body)
			} else {
				doc.styles = append(doc.styles, body)
			}
			i = bodyEnd
		}
		doc.tags = append(doc.tags, t)
	}
}

// parseTag parses `name attr="v" attr='v' attr=v attr>` starting just after '<'. It returns the
// tag and the index just after its '>'.
func parseTag(html, lower string, start int) (tag, int) {
	n := len(html)
	isName := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == ':' || c == '_'
	}
	i := start
	for i < n && isName(html[i]) {
		i++
	}
	t := tag{name: lower[start:i]}
	for {
		for i < n && (isSpace(html[i]) || html[i] == '/') {
			i++
		}
		if i >= n {
			return t, n
		}
		if html[i] == '>' {
			return t, i + 1
		}
		nameStart := i
		for i < n && !isSpace(html[i]) && html[i] != '=' && html[i] != '>' {
			i++
		}
		name := lower[nameStart:i]
		value := ""
		if i < n && html[i] == '=' {
			i++
			if i < n && (html[i] == '"' || html[i] == '\'') {
				quote := html[i]
				vStart := i + 1
				vEnd := n
				if e := strings.IndexByte(html[vStart:], quote); e >= 0 {
					vEnd = vStart + e
				}
				value = html[vStart:vEnd]
				i = min(vEnd+1, n)
			} else {
				vStart := i
				for i < n && !isSpace(html[i]) && html[i] != '>' {
					i++
				}
				value = html[vStart:i]
			}
		}
		t.attrs = append(t.attrs, attr{name, value})
	}
}
