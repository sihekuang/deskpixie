package agent

import (
	"reflect"
	"strings"
	"testing"
)

// Ported from crates/agent-core/src/spoken.rs (20 tests).

func one(ref string, hasRef bool, status SpokenStatus, text string) Spoken {
	s := Spoken{Status: status, Text: text}
	if hasRef {
		s.Reference = &ref
	}
	return s
}

func eqSpoken(t *testing.T, got, want []Spoken) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %s, want %s", fmtSpoken(got), fmtSpoken(want))
	}
}

func fmtSpoken(s []Spoken) string {
	var b strings.Builder
	for _, x := range s {
		ref := "<nil>"
		if x.Reference != nil {
			ref = *x.Reference
		}
		b.WriteString("{" + ref + " " + x.Status.String() + " " + x.Text + "}")
	}
	return b.String()
}

func TestABlockWithRefAndStatus(t *testing.T) {
	msg := "Did the work.\n<spoken ref=\"job_0123456789ab\" status=\"done\">All 12 tests pass now.</spoken>"
	eqSpoken(t, ParseSpoken(msg), []Spoken{one("job_0123456789ab", true, SpokenDone, "All 12 tests pass now.")})
}

func TestSeveralBlocksAreAllReturnedInOrder(t *testing.T) {
	msg := "<spoken ref='job_a' status='working'>Still digging.</spoken> text " +
		"<spoken ref=\"job_b\" status=\"question\">Which branch?</spoken>"
	got := ParseSpoken(msg)
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	eqSpoken(t, got[:1], []Spoken{one("job_a", true, SpokenWorking, "Still digging.")})
	eqSpoken(t, got[1:], []Spoken{one("job_b", true, SpokenQuestion, "Which branch?")})
}

func TestMissingOrUnknownAttributesAreNone(t *testing.T) {
	eqSpoken(t, ParseSpoken("<spoken>Hi there.</spoken>"), []Spoken{one("", false, SpokenNone, "Hi there.")})
	eqSpoken(t, ParseSpoken("<spoken status=\"maybe\" ref=\"job_x\">Hm.</spoken>"), []Spoken{one("job_x", true, SpokenNone, "Hm.")})
	if ParseSpoken("<spoken href=\"x\">Hm.</spoken>")[0].Reference != nil {
		t.Fatal("href is not ref")
	}
}

func TestMarkupIsStripped(t *testing.T) {
	msg := "<spoken ref=\"job_a\">**Done**: fixed `upload_test` in <b>focus</b>.</spoken>"
	if got := ParseSpoken(msg)[0].Text; got != "Done: fixed upload test in focus." {
		t.Fatalf("got %q", got)
	}
}

func TestTextIsCappedAtTwoSentences(t *testing.T) {
	if got := ParseSpoken("<spoken>One. Two! Three? Four.</spoken>")[0].Text; got != "One. Two!" {
		t.Fatalf("got %q", got)
	}
}

func TestTextIsCappedAt35WordsOnASentenceBoundary(t *testing.T) {
	first := strings.Repeat("word ", 19) + "word."
	second := strings.Repeat("more ", 19) + "more."
	msg := "<spoken>" + first + " " + second + "</spoken>"
	if got := ParseSpoken(msg)[0].Text; got != first {
		t.Fatalf("the second sentence would pass 35 words: %q", got)
	}
}

func TestAnOverlongFirstSentenceIsCutAt35Words(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 50))
	text := ParseSpoken("<spoken>" + long + ".</spoken>")[0].Text
	if n := len(strings.Fields(text)); n != 35 {
		t.Fatalf("words %d", n)
	}
	if !strings.HasSuffix(text, "word…") {
		t.Fatalf("got %q", text)
	}
}

func TestDecimalsAndPathsDoNotEndSentences(t *testing.T) {
	if got := Cap("Version 1.2 is out. It works. Really."); got != "Version 1.2 is out. It works." {
		t.Fatalf("got %q", got)
	}
}

func TestEmptyBlocksAreSkippedAndUnclosedBlocksRunToTheEnd(t *testing.T) {
	if len(ParseSpoken("<spoken ref=\"job_a\">  </spoken>")) != 0 {
		t.Fatal("empty block")
	}
	if got := ParseSpoken("<spoken ref=\"job_a\">Half done")[0].Text; got != "Half done" {
		t.Fatalf("got %q", got)
	}
	if len(ParseSpoken("<spokenword>no</spokenword>")) != 0 {
		t.Fatal("spokenword")
	}
}

func TestNoBlockMeansEmpty(t *testing.T) {
	if len(ParseSpoken("Just a plain reply.")) != 0 {
		t.Fatal("not empty")
	}
}

func TestFallbackTakesTheFirstTwoSentencesWithoutCode(t *testing.T) {
	msg := "I compared them. Rust wins on speed.\n```rust\nfn main() {}\n```\nDetails follow."
	if got := SpokenFallback(msg); got != "I compared them. Rust wins on speed." {
		t.Fatalf("got %q", got)
	}
	if got := SpokenFallback("```\nonly code\n```"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := SpokenFallback("# Heading\n\nThe *answer* is 42."); got != "Heading The answer is 42." {
		t.Fatalf("got %q", got)
	}
}

func TestAStrayLessThanKeepsTheText(t *testing.T) {
	if got := Clean("if a < b then done."); got != "if a < b then done." {
		t.Fatalf("got %q", got)
	}
	if got := Clean("a <b never closes"); got != "a <b never closes" {
		t.Fatalf("got %q", got)
	}
	if got := ParseSpoken("<spoken>if a < b then done.</spoken>")[0].Text; got != "if a < b then done." {
		t.Fatalf("got %q", got)
	}
}

func TestGenericsDoNotLoseTheTextAfterThem(t *testing.T) {
	if got := Clean("Use Vec<String> and then stop."); !strings.HasSuffix(got, "and then stop.") {
		t.Fatalf("got %q", got)
	}
}

func TestMultibyteBodiesAreCappedWithoutPanicking(t *testing.T) {
	got := ParseSpoken("<spoken ref=\"job_x\">日本語です。次の文。三つ目。</spoken>")
	if len(got) != 1 || got[0].Text == "" {
		t.Fatalf("got %v", got)
	}
	if len(ParseSpoken(strings.Repeat("é", 70_000))) != 0 {
		t.Fatal("no block expected")
	}
	if SpokenFallback(strings.Repeat("é", 40_000)) == "" {
		t.Fatal("fallback empty")
	}
}

func TestHugeFenceInputIsHandled(t *testing.T) {
	huge := strings.Repeat("``` ```", 150_000)
	if len(ParseSpoken(huge)) != 0 || SpokenFallback(huge) != "" || Clean(huge) != "" {
		t.Fatal("huge fence")
	}
}

func TestQuotedValuesCannotInjectAttributes(t *testing.T) {
	eqSpoken(t, ParseSpoken("<spoken note=\"x ref='job_evil'\" status=\"done\">Hi.</spoken>"), []Spoken{one("", false, SpokenDone, "Hi.")})
	eqSpoken(t, ParseSpoken("<spoken ref=\"job_a\" note='status=\"failed\"'>Hi.</spoken>"), []Spoken{one("job_a", true, SpokenNone, "Hi.")})
}

func TestGreaterThanInsideAQuotedAttributeDoesNotEndTheTag(t *testing.T) {
	eqSpoken(t, ParseSpoken("<spoken ref=\"job_a>b\" status=\"done\">Hi.</spoken>"), []Spoken{one("job_a>b", true, SpokenDone, "Hi.")})
}

func TestAnUnterminatedOpenTagYieldsNothing(t *testing.T) {
	if len(ParseSpoken("<spoken ref=\"job_a\"")) != 0 || len(ParseSpoken("<spoken ref=\"job_a>Hi.</spoken>")) != 0 {
		t.Fatal("unterminated")
	}
}

func TestCodeInsideASpokenBodyIsDropped(t *testing.T) {
	msg := "<spoken ref=\"job_a\">Fixed it. ```rust\nfn main() {}\n``` Tests pass. Extra.</spoken>"
	if got := ParseSpoken(msg)[0].Text; got != "Fixed it. Tests pass." {
		t.Fatalf("got %q", got)
	}
}

func TestInvisibleFormatCharactersAreDropped(t *testing.T) {
	if got := Clean("a\u200Bb \u202Ec\u2066d\uFEFF"); got != "ab cd" {
		t.Fatalf("got %q", got)
	}
}
