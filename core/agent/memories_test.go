package agent_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	. "github.com/sihekuang/deskpixie/core/agent"
	"github.com/sihekuang/deskpixie/core/agent/agenttest"
)

// Every finished coordinator turn leaves exactly one short memory.

func memories(t *testing.T, store *agenttest.MemStore) []Memory {
	t.Helper()
	var out []Memory
	for _, line := range store.Lines(MemoriesFile) {
		var m Memory
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(line, err)
		}
		out = append(out, m)
	}
	return out
}

func wantShort(t *testing.T, text string) {
	t.Helper()
	if text == "" || len(text) > MemoryMaxBytes || len(strings.Fields(text)) > MemoryMaxWords {
		t.Fatalf("%d bytes, %d words: %q", len(text), len(strings.Fields(text)), text)
	}
}

func TestATurnWithAMemoryBlockKeepsItAndNeverSpeaksIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		r.voice.SetOpen(true)
		delegate(t, r, "pick a logo")
		settle()
		stopHook(r, `Done.
<spoken ref="job_000000000001" status="done">I picked the blue logo.</spoken>
<memory ref="job_000000000001">The person wanted a logo; I chose the blue one for its contrast.</memory>`)
		settle()
		got := memories(t, r.store)
		if len(got) != 1 || got[0].Text != "The person wanted a logo; I chose the blue one for its contrast." ||
			got[0].Source != MemorySourceCoordinator || got[0].Job == nil || *got[0].Job != "job_000000000001" {
			t.Fatal(got)
		}
		for _, c := range r.voice.Calls() {
			if strings.Contains(fmt.Sprint(c), "contrast") {
				t.Fatal("a memory was spoken:", c)
			}
		}
	})
}

func TestATurnWithoutAMemoryGetsAFallbackFromTheTaskAndWhatWasSaid(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "count the open issues")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="done">There are 12. Three are bugs.</spoken>`)
		settle()
		// A turn about no job (the person typed in the terminal), with no <spoken> block at all.
		stopHook(r, "I tidied the notes folder as you asked.")
		settle()
		got := memories(t, r.store)
		if len(got) != 2 {
			t.Fatal(got)
		}
		if got[0].Text != "count the open issues: There are 12." || got[0].Source != MemorySourceFallback ||
			got[0].Job == nil || *got[0].Job != "job_000000000001" {
			t.Fatal(got[0])
		}
		if got[1].Text != "I tidied the notes folder as you asked." || got[1].Job != nil {
			t.Fatal(got[1])
		}
		// Even an empty turn leaves one.
		stopHook(r, "")
		settle()
		if got := memories(t, r.store); len(got) != 3 || got[2].Text == "" {
			t.Fatal(got)
		}
	})
}

func TestAnOverLongMemoryIsCutToOneShortSentence(t *testing.T) {
	long := strings.Repeat("word ", 60) + "end. A second sentence."
	text, _, ok := ParseMemory("<memory>**" + long + "**</memory>")
	if !ok || !strings.HasSuffix(text, "…") || strings.Contains(text, "second") || strings.Contains(text, "*") {
		t.Fatal(text)
	}
	wantShort(t, text)
	// Long words hit the byte cap before the word cap.
	text = CapMemory(strings.Repeat("supercalifragilistic ", 20))
	wantShort(t, text)
	if !strings.HasSuffix(text, "…") || !utf8.ValidString(text) {
		t.Fatal(text)
	}
	text = CapMemory(strings.Repeat("é", 400))
	wantShort(t, text)
	if !utf8.ValidString(text) {
		t.Fatal(text)
	}
	if got := CapMemory("  Kept   as is.  "); got != "Kept as is." {
		t.Fatal(got)
	}
	// The fallback is capped the same way.
	wantShort(t, FallbackMemory(strings.Repeat("a long task ", 30), strings.Repeat("and a long reply ", 30)))
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		stopHook(r, "<spoken>Ok.</spoken><memory>"+long+"</memory>")
		settle()
		got := memories(t, r.store)
		if len(got) != 1 {
			t.Fatal(got)
		}
		wantShort(t, got[0].Text)
	})
}

func TestMemoriesSurviveARestartAndTheOldestAreTrimmed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &agenttest.MemStore{}
		var lines []string
		for n := range MemoryMaxKept + 5 {
			lines = append(lines, fmt.Sprintf(`{"at":%d,"text":"memory %d.","source":"coordinator"}`, n, n))
		}
		lines = append(lines, "not json", `{"at":1,"text":"`+strings.Repeat("x ", 300)+`","source":"coordinator"}`)
		store.Rewrite(MemoriesFile, lines)
		r := rigWith(t, store, false)
		settle()
		got := memories(t, store)
		if len(got) != MemoryMaxKept || got[0].Text != "memory 6." {
			t.Fatal(len(got), got[0])
		}
		wantShort(t, got[len(got)-1].Text)
		stopHook(r, "<spoken>One more.</spoken><memory>The newest one.</memory>")
		settle()
		got = memories(t, store)
		if len(got) != MemoryMaxKept || got[0].Text != "memory 7." || got[len(got)-1].Text != "The newest one." {
			t.Fatal(len(got), got[0], got[len(got)-1])
		}
		again := rigWith(t, store, false)
		settle()
		if f := again.h.Facts(); !strings.Contains(f, "The newest one.") {
			t.Fatal(f)
		}
	})
}

func TestBitsContextHasTheMemoriesAndRecallCarriesThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig(t)
		delegate(t, r, "pick a logo")
		settle()
		stopHook(r, `<spoken ref="job_000000000001" status="done">Blue it is.</spoken>`)
		settle()
		stopHook(r, `<spoken>Noted.</spoken><memory>The person likes short weekly summaries on Fridays.</memory>`)
		settle()
		f := r.h.Facts()
		if !strings.Contains(f, "done: pick a logo. Reply: Blue it is.") ||
			!strings.Contains(f, "The person likes short weekly summaries on Fridays.") {
			t.Fatal(f)
		}
		// The fallback memory of a job already listed is not repeated.
		if strings.Count(f, "Blue it is.") != 1 {
			t.Fatal(f)
		}
		invoke(t, Recall{H: r.h}, `{"question":"what does the person like?"}`)
		r.clock.Advance(StopSettleMs)
		time.Sleep(2 * time.Second)
		prompts := r.host.Prompts()
		last := prompts[len(prompts)-1].Text
		if !strings.Contains(last, "Your saved memories") || !strings.Contains(last, "short weekly summaries on Fridays") ||
			!strings.Contains(last, "pick a logo: Blue it is. [job_000000000001]") {
			t.Fatal(last)
		}
	})
}

func TestMemoryFactsFitTheBudgetWithJobsAndNotes(t *testing.T) {
	var jobs []Job
	var notes []Memory
	for n := uint64(1); n <= 30; n++ {
		j := memJob(n, fmt.Sprintf("task %d %s", n, strings.Repeat("x", 80)), JobDone, memNow-Millis(30-n)*60_000)
		j.Reply = strp(strings.Repeat("reply ", 30))
		jobs = append(jobs, j)
		notes = append(notes, Memory{At: memNow - Millis(30-n)*60_000, Text: CapMemory(fmt.Sprintf("note %d %s", n, strings.Repeat("y ", 40))), Source: MemorySourceCoordinator})
	}
	got := MemoryFacts(jobs, notes, memNow, time.UTC)
	if utf8.RuneCountInString(got) > MemoryBudgetChars || !strings.Contains(got, "note 30 ") || !strings.Contains(got, "task 30 ") {
		t.Fatal(utf8.RuneCountInString(got), got)
	}
	if only := MemoryFacts(nil, notes[:1], memNow, time.UTC); !strings.HasPrefix(only, "Recent work with the agent") {
		t.Fatal(only)
	}
}
