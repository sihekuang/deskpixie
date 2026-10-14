package canvas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped design-system pages pass the linter (spec section 1 success criteria, 8.1).
func ds(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "design-system", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestShippedExamplesLintClean(t *testing.T) {
	for _, name := range []string{"dashboard", "architecture", "comparison"} {
		if w := Lint(ds(t, "examples/"+name+".html")); len(w) != 0 {
			t.Errorf("%s.html: %v", name, w)
		}
	}
}

func TestEmptyStatePageLintsClean(t *testing.T) {
	if w := Lint(ds(t, "empty.html")); len(w) != 0 {
		t.Error(w)
	}
}

func TestCanvasMdSkeletonLintsClean(t *testing.T) {
	guide := ds(t, "CANVAS.md")
	i := strings.Index(guide, "```html\n")
	if i < 0 {
		t.Fatal("CANVAS.md has an html block")
	}
	start := i + len("```html\n")
	end := start + strings.Index(guide[start:], "```")
	if w := Lint(guide[start:end]); len(w) != 0 {
		t.Error(w)
	}
}
