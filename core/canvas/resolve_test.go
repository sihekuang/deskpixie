package canvas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture: <tmp>/ds/{tokens.css, vendor/chart.umd.min.js, .hidden}, <tmp>/pages/{report.html, Bad.html},
// <tmp>/secret.txt, plus symlinks that try to escape.
func resolverFixture(t *testing.T) (string, *Resolver) {
	t.Helper()
	tmp := t.TempDir()
	ds, pages := filepath.Join(tmp, "ds"), filepath.Join(tmp, "pages")
	for _, d := range []string{filepath.Join(ds, "vendor"), pages} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(ds, "tokens.css"), []byte(":root{}"))
	writeFile(t, filepath.Join(ds, "vendor/chart.umd.min.js"), []byte("/*chart*/"))
	writeFile(t, filepath.Join(ds, ".hidden"), []byte("x"))
	writeFile(t, filepath.Join(pages, "report.html"), []byte("<title>R</title>"))
	writeFile(t, filepath.Join(pages, "Bad.html"), []byte("x"))
	writeFile(t, filepath.Join(tmp, "secret.txt"), []byte("secret"))
	for from, to := range map[string]string{
		filepath.Join(tmp, "secret.txt"): filepath.Join(pages, "link.html"),
	} {
		if err := os.Symlink(from, to); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Symlink(filepath.Join(tmp, "secret.txt"), filepath.Join(ds, "evil.css"))
	_ = os.Symlink(tmp, filepath.Join(ds, "up"))
	return tmp, NewResolver(ds, pages)
}

func resolveErr(t *testing.T, r *Resolver, path string) error {
	t.Helper()
	_, err := r.ResolveFile(path)
	if err == nil {
		t.Fatalf("%q resolved", path)
	}
	return err
}

func TestServesDesignSystemFiles(t *testing.T) {
	_, r := resolverFixture(t)
	got, err := r.ResolveFile("/ds/tokens.css")
	if err != nil || got.Area != AreaDS || !strings.HasSuffix(got.File, "ds/tokens.css") || got.Page != nil {
		t.Fatal(got, err)
	}
	got, err = r.ResolveFile("/ds/vendor/chart.umd.min.js")
	if err != nil || !strings.HasSuffix(got.File, "vendor/chart.umd.min.js") {
		t.Fatal(got, err)
	}
}

func TestServesValidPages(t *testing.T) {
	_, r := resolverFixture(t)
	got, err := r.ResolveFile("/pages/report.html")
	if err != nil || got.Area != AreaPages || got.Page == nil || got.Page.Slug() != "report" {
		t.Fatal(got, err)
	}
}

func TestRejectsDotDot(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{"/ds/../secret.txt", "/pages/../secret.txt", "/ds/vendor/../../secret.txt", "/ds/./tokens.css"} {
		if err := resolveErr(t, r, p); err != ErrBadPath {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestRejectsEncodedTraversal(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{
		"/ds/%2e%2e/secret.txt", "/ds/%2E%2E/secret.txt", "/ds/..%2fsecret.txt", "/ds/%2e%2e%2fsecret.txt",
		"/ds/vendor%2f..%2f..%2fsecret.txt", "/ds/%5c..%5csecret.txt", "/pages/%2e%2e%2fsecret.txt",
	} {
		if err := resolveErr(t, r, p); err != ErrBadPath {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Double encoding decodes once to a literal "%2e%2e" name, which does not exist.
	if err := resolveErr(t, r, "/ds/%252e%252e/secret.txt"); err != ErrNotFound {
		t.Error(err)
	}
}

func TestRejectsAbsoluteAndEmptySegments(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{"/ds//etc/passwd", "/ds/tokens.css/", "//ds/tokens.css", "ds/tokens.css", "", "/ds/tokens.css%00", "/ds/C:%5cwindows"} {
		resolveErr(t, r, p)
	}
	if err := resolveErr(t, r, "/ds//etc/passwd"); err != ErrBadPath {
		t.Error(err)
	}
	if err := resolveErr(t, r, "ds/tokens.css"); err != ErrBadPath {
		t.Error(err)
	}
}

func TestRejectsSymlinkEscape(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{"/pages/link.html", "/ds/evil.css", "/ds/up/secret.txt"} {
		if err := resolveErr(t, r, p); err != ErrEscapes {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestPagesMustBeOneValidSlug(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{"/pages/Bad.html", "/pages/sub/x.html", "/pages/missing.html", "/pages/", "/pages", "/other/x", "/"} {
		if err := resolveErr(t, r, p); err != ErrNotFound && err != ErrBadPath {
			t.Errorf("%s: %v", p, err)
		}
	}
	if err := resolveErr(t, r, "/pages/Bad.html"); err != ErrNotFound {
		t.Error(err)
	}
	if err := resolveErr(t, r, "/other/x"); err != ErrNotFound {
		t.Error(err)
	}
}

func TestRejectsDotfilesAndDirectories(t *testing.T) {
	_, r := resolverFixture(t)
	if err := resolveErr(t, r, "/ds/.hidden"); err != ErrBadPath {
		t.Error(err)
	}
	if err := resolveErr(t, r, "/ds/vendor"); err != ErrNotFound {
		t.Error(err)
	}
}

func TestRejectsBadPercentEscapes(t *testing.T) {
	_, r := resolverFixture(t)
	for _, p := range []string{"/ds/%zz", "/ds/%2", "/ds/%ff", "/ds/%+1x"} {
		if err := resolveErr(t, r, p); err != ErrBadPath {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := r.ResolveFile("/ds/tok%65ns.css"); err != nil {
		t.Error("plain escapes still work")
	}
}

func TestPagesMustBeExactPlainFiles(t *testing.T) {
	tmp, r := resolverFixture(t)
	if err := os.Mkdir(filepath.Join(tmp, "pages/dir.html"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := resolveErr(t, r, "/pages/dir.html"); err != ErrNotFound {
		t.Error(err)
	}
	// On a case-insensitive file system mixed.html opens Mixed.html; the spelling check refuses it.
	writeFile(t, filepath.Join(tmp, "pages/Mixed.html"), []byte("x"))
	if err := resolveErr(t, r, "/pages/mixed.html"); err != ErrNotFound {
		t.Error(err)
	}
}

func TestContentTypesByExtension(t *testing.T) {
	for in, want := range map[string]string{
		"a.html": "text/html; charset=utf-8", "a.CSS": "text/css; charset=utf-8",
		"vendor/chart.umd.min.js": "text/javascript; charset=utf-8", "CANVAS.md": "text/markdown; charset=utf-8",
		"x.svg": "image/svg+xml", "x.png": "image/png", "x.woff2": "font/woff2", "LICENSE": "application/octet-stream",
	} {
		if got := ContentType(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// Additions beyond the Rust resolve.rs tests: the Response shape that replaces
// src-tauri/src/canvas/protocol.rs's `respond` (its header/status/cap rules are tested by S5 in Swift
// for the WebKit parts; the pure parts live here).

func TestResolveReturnsFileHeadersAndStatus(t *testing.T) {
	_, r := resolverFixture(t)
	resp, err := r.Resolve("/ds/tokens.css")
	if err != nil || resp.Status != 200 || resp.ContentType != "text/css; charset=utf-8" || !strings.HasSuffix(resp.File, "ds/tokens.css") {
		t.Fatal(resp, err)
	}
	if resp.Headers["Content-Security-Policy"] != CSP || resp.Headers["X-Content-Type-Options"] != "nosniff" ||
		resp.Headers["Referrer-Policy"] != "no-referrer" || resp.Headers["Cache-Control"] != "no-store" {
		t.Fatal(resp.Headers)
	}
	for _, p := range []string{"/ds/../secret.txt", "/pages/link.html", "/nope"} {
		resp, err := r.Resolve(p)
		if err != nil || resp.Status != 404 || resp.File != "" || resp.Headers["Content-Security-Policy"] != CSP {
			t.Errorf("%s: %v %v", p, resp, err)
		}
	}
}

func TestResolveRefusesPagesOverTheCap(t *testing.T) {
	tmp, r := resolverFixture(t)
	writeFile(t, filepath.Join(tmp, "pages/big.html"), []byte(strings.Repeat("x", int(MaxPageBytes)+1)))
	if resp, _ := r.Resolve("/pages/big.html"); resp.Status != 404 {
		t.Fatal(resp)
	}
	if resp, _ := r.Resolve("/pages/report.html"); resp.Status != 200 {
		t.Fatal(resp)
	}
}

func TestWithMetaCSPGoesAfterTheDoctype(t *testing.T) {
	meta := `<meta http-equiv="Content-Security-Policy" content="` + CSP + `">`
	if got := string(WithMetaCSP([]byte("<!DOCTYPE html><html></html>"))); got != "<!DOCTYPE html>"+meta+"<html></html>" {
		t.Error(got)
	}
	if got := string(WithMetaCSP([]byte("<p>x</p>"))); got != meta+"<p>x</p>" {
		t.Error(got)
	}
	if got := string(WithMetaCSP([]byte("\xef\xbb\xbf <!doctype html><p>"))); got != "\xef\xbb\xbf <!doctype html>"+meta+"<p>" {
		t.Error(got)
	}
}
