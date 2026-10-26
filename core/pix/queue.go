package pix

import (
	"errors"
	"sync"
)

// MaxQueue is how many messages wait for one agent at most.
const MaxQueue = 20

var ErrQueueFull = errors.New("busy: 20 messages waiting")

// Queue holds messages for the agent until they can be delivered. Each message pushed gets the
// next number (#339), so DeskPixie can tell which of them a delivery covers.
type Queue struct {
	mu   sync.Mutex
	msgs []string
	seqs []uint64 // each message's number
	last uint64   // the last number given
	told uint64   // the last Delivered said
}

// Push queues s; it returns its number.
func (q *Queue) Push(s string) (uint64, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) >= MaxQueue {
		return 0, ErrQueueFull
	}
	q.last++
	q.msgs, q.seqs = append(q.msgs, s), append(q.seqs, q.last)
	return q.last, nil
}

func (q *Queue) Drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs, q.seqs = nil, nil
	return out
}

// Requeue puts msgs back in front, to go first (#306: a menu kept them from being sent). Their
// numbers are lost: they count as next after the last delivery told, so a later Delivered
// never covers them before they go.
func (q *Queue) Requeue(msgs []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seqs := make([]uint64, len(msgs))
	for i := range seqs {
		seqs[i] = q.told + 1
	}
	q.msgs = append(append([]string(nil), msgs...), q.msgs...)
	q.seqs = append(seqs, q.seqs...)
}

// Last is the number of the last message pushed (0: none yet).
func (q *Queue) Last() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.last
}

// Delivered is the highest number every message up to which has reached the agent: all pushed
// but those still queued. held: some are out of the queue but not delivered (a menu stopped
// them), so it says no more than last time. 0 is none yet: pix registers with a Run, so DeskPixie
// doesn't take it for "all of them" as from a pix too old to number its messages (#343).
func (q *Queue) Delivered(held bool) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	upTo := q.last
	for _, n := range q.seqs {
		upTo = min(upTo, n-1)
	}
	if held {
		upTo = q.told
	}
	q.told = max(q.told, upTo)
	return q.told
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}
