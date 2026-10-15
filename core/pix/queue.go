package pix

import (
	"errors"
	"sync"
)

// MaxQueue is how many messages wait for one agent at most.
const MaxQueue = 20

var ErrQueueFull = errors.New("busy: 20 messages waiting")

// Queue holds messages for the agent until they can be delivered.
type Queue struct {
	mu   sync.Mutex
	msgs []string
}

func (q *Queue) Push(s string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.msgs) >= MaxQueue {
		return ErrQueueFull
	}
	q.msgs = append(q.msgs, s)
	return nil
}

func (q *Queue) Drain() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.msgs
	q.msgs = nil
	return out
}

func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.msgs)
}
