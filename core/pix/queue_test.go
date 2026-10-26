package pix

import "testing"

// #339: each message pushed gets the next number; Delivered is the highest number every
// message up to which has left the queue, never past one a menu sent back, or one held out.
func TestTheQueueNumbersMessagesAndWhatWentIn(t *testing.T) {
	var q Queue
	if n := q.Delivered(false); n != 0 {
		t.Fatalf("nothing pushed: %d", n)
	}
	a, _ := q.Push("a")
	b, _ := q.Push("b")
	if a != 1 || b != 2 || q.Last() != 2 {
		t.Fatalf("%d %d %d", a, b, q.Last())
	}
	if n := q.Delivered(false); n != 0 {
		t.Fatalf("both still queued: %d", n)
	}
	msgs := q.Drain()
	c, _ := q.Push("c") // comes in while a and b are typed
	if n := q.Delivered(false); n != 2 || c != 3 {
		t.Fatalf("a and b went, c waits: %d", n)
	}
	q.Requeue(msgs[1:]) // a menu sent b back (and c waits behind it)
	if n := q.Delivered(false); n != 2 {
		t.Fatalf("never back past what was told: %d", n)
	}
	q.Drain()
	if n := q.Delivered(true); n != 2 {
		t.Fatalf("held out by a menu: %d", n)
	}
	if n := q.Delivered(false); n != 3 {
		t.Fatalf("all of them went: %d", n)
	}
}
