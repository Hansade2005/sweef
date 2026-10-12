package evqueue

import (
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell"
)

func TestCoalesceAndOrder(t *testing.T) {
	q := New()
	defer q.Close()
	// Fill the consumer-side buffer so later events sit in the queue.
	for i := 0; i < 20; i++ {
		q.Push(shell.Event{Kind: shell.EvKeyDown, Key: "a"})
	}
	q.Push(shell.Event{Kind: shell.EvMouseMove, X: 1})
	q.Push(shell.Event{Kind: shell.EvMouseMove, X: 2})
	q.Push(shell.Event{Kind: shell.EvWheel, DY: 10})
	q.Push(shell.Event{Kind: shell.EvWheel, DY: 5})
	q.Push(shell.Event{Kind: shell.EvKeyUp, Key: "a"})
	var got []shell.Event
	timeout := time.After(2 * time.Second)
	for len(got) < 20+3 {
		select {
		case e := <-q.Events():
			got = append(got, e)
		case <-timeout:
			t.Fatalf("timeout, got %d", len(got))
		}
	}
	if got[20].Kind != shell.EvMouseMove || got[20].X != 2 {
		t.Fatalf("move not coalesced to last: %+v", got[20])
	}
	if got[21].Kind != shell.EvWheel || got[21].DY != 15 {
		t.Fatalf("wheel not summed: %+v", got[21])
	}
	if got[22].Kind != shell.EvKeyUp {
		t.Fatalf("order broken: %+v", got[22])
	}
}

func TestClose(t *testing.T) {
	q := New()
	q.Close()
	q.Push(shell.Event{})
	select {
	case _, ok := <-q.Events():
		if ok {
			t.Fatal("event after close")
		}
	case <-time.After(time.Second):
		t.Fatal("not closed")
	}
}
