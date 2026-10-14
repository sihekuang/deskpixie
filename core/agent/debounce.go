package agent

import (
	"fmt"
	"slices"
	"strings"
)

// Updates debounces what the user hears about a job's workers and progress, so several busy
// workers don't become a stream of interruptions:
//   - dedupe: a line already said for a job (same text after normalising case, spaces and end
//     punctuation) is not said again within UpdateDedupeMs;
//   - coalesce: a job's worker reports and progress are held until the job has been quiet for
//     UpdateQuietMs (at most UpdateMaxHoldMs), then said as one line: the reports (the latest
//     per worker, combined), or else the latest progress; a report beats progress;
//   - supersede: the coordinator's (checked) reply for the job drops what is still held.
// Questions never come here. What comes out still goes through the Router's per-minute cap.

const (
	// UpdateQuietMs: a job's held updates go out once nothing new came for it for this long.
	UpdateQuietMs Millis = 20_000
	// UpdateMaxHoldMs: a job that keeps reporting is still heard this often.
	UpdateMaxHoldMs Millis = 30_000
	// UpdateDedupeMs: an identical line for the same job is not said again within this window.
	UpdateDedupeMs Millis = 10 * 60_000
)

type heldReport struct {
	worker, text string
}

type heldUpdate struct {
	first, last Millis
	// reports: the latest report of each worker, in the order the workers first reported.
	reports  []heldReport
	progress string
}

type saidLine struct {
	norm string
	at   Millis
}

// DueUpdate is one job's coalesced update, ready to be said.
type DueUpdate struct {
	Job  JobID
	Text string
	// Progress: Text is a progress phrase (narration), not worker reports.
	Progress bool
}

type Updates struct {
	held  map[JobID]*heldUpdate
	order []JobID
	said  map[JobID][]saidLine
}

func NewUpdates() *Updates {
	return &Updates{held: map[JobID]*heldUpdate{}, said: map[JobID][]saidLine{}}
}

// normLine is a line's text for dedupe: lowercase, single spaces, no end punctuation.
func normLine(s string) string {
	return strings.TrimRight(strings.Join(strings.Fields(strings.ToLower(s)), " "), ".!?…;:, ")
}

// AddReport holds a worker's report line for job; false if it is a repeat (dropped).
func (u *Updates) AddReport(job JobID, worker, text string, now Millis) bool {
	h, ok := u.add(job, text, now)
	if !ok {
		return false
	}
	if i := slices.IndexFunc(h.reports, func(r heldReport) bool { return r.worker == worker }); i >= 0 {
		h.reports[i].text = text
	} else {
		h.reports = append(h.reports, heldReport{worker, text})
	}
	return true
}

// AddProgress holds a progress phrase for job (the latest wins); false if it is a repeat.
func (u *Updates) AddProgress(job JobID, phrase string, now Millis) bool {
	h, ok := u.add(job, phrase, now)
	if ok {
		h.progress = phrase
	}
	return ok
}

func (u *Updates) add(job JobID, text string, now Millis) (*heldUpdate, bool) {
	norm := normLine(text)
	if norm == "" || u.repeats(job, norm, now) {
		return nil, false
	}
	h := u.held[job]
	if h == nil {
		h = &heldUpdate{first: now}
		u.held[job] = h
		u.order = append(u.order, job)
	}
	h.last = now
	return h, true
}

// repeats: norm was said for job within the dedupe window, or is already held for it.
func (u *Updates) repeats(job JobID, norm string, now Millis) bool {
	for _, s := range u.said[job] {
		if s.norm == norm && satSub(now, s.at) < UpdateDedupeMs {
			return true
		}
	}
	if h := u.held[job]; h != nil {
		if normLine(h.progress) == norm {
			return true
		}
		for _, r := range h.reports {
			if normLine(r.text) == norm {
				return true
			}
		}
	}
	return false
}

// Due takes the jobs whose updates are ready (quiet long enough, or held the longest allowed)
// and remembers them as said. A job holding only progress waits until ready(job) is true too
// (the narrator's interval).
func (u *Updates) Due(now Millis, ready func(JobID) bool) []DueUpdate {
	u.forgetOld(now)
	var out []DueUpdate
	for _, job := range slices.Clone(u.order) {
		h := u.held[job]
		if satSub(now, h.last) < UpdateQuietMs && satSub(now, h.first) < UpdateMaxHoldMs {
			continue
		}
		if len(h.reports) == 0 && !ready(job) {
			continue
		}
		u.Supersede(job)
		if len(h.reports) == 0 {
			u.remember(job, h.progress, now)
			out = append(out, DueUpdate{Job: job, Text: h.progress, Progress: true})
			continue
		}
		texts := make([]string, len(h.reports))
		for i, r := range h.reports {
			texts[i] = r.text
			u.remember(job, r.text, now)
		}
		out = append(out, DueUpdate{Job: job, Text: combineReports(texts)})
	}
	return out
}

// combineReports: one report as is; several as "2 worker updates: <a> <b>".
func combineReports(texts []string) string {
	if len(texts) == 1 {
		return texts[0]
	}
	return fmt.Sprintf("%d worker updates: %s", len(texts), strings.Join(texts, " "))
}

func (u *Updates) remember(job JobID, text string, now Millis) {
	u.said[job] = append(u.said[job], saidLine{normLine(text), now})
}

// Supersede drops what is held for job (the coordinator replied for it, or it ended). What was
// already said stays remembered for dedupe.
func (u *Updates) Supersede(job JobID) {
	if _, ok := u.held[job]; !ok {
		return
	}
	delete(u.held, job)
	u.order = slices.DeleteFunc(u.order, func(j JobID) bool { return j == job })
}

// Clear drops everything held (shutdown, delegation off).
func (u *Updates) Clear() {
	u.held = map[JobID]*heldUpdate{}
	u.order = nil
}

func (u *Updates) forgetOld(now Millis) {
	for job, lines := range u.said {
		lines = slices.DeleteFunc(lines, func(s saidLine) bool { return satSub(now, s.at) >= UpdateDedupeMs })
		if len(lines) == 0 {
			delete(u.said, job)
		} else {
			u.said[job] = lines
		}
	}
}
