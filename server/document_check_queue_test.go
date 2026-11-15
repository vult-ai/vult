package server

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func waitDocumentCheck[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatal("document check did not advance")
		var zero T
		return zero
	}
}

func TestDocumentCheckQueueBoundsAndOrdersAcceptedWork(t *testing.T) {
	q := newDocumentCheckQueue()
	t.Cleanup(q.cancel)
	started := make(chan int, documentCheckCapacity)
	release := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})}
	keys := []documentCheckKey{{document: "a"}, {document: "b"}, {document: "a", reading: [32]byte{1}}, {document: "a", reading: [32]byte{2}}}
	start := func(i int) {
		t.Helper()
		if !q.reserve() || !q.start(t.Context(), keys[i], func(ctx context.Context) {
			started <- i
			select {
			case <-release[i]:
			case <-ctx.Done():
			}
		}) {
			t.Fatal("accepted work was refused")
		}
	}
	start(0)
	start(1)
	first, second := waitDocumentCheck(t, started), waitDocumentCheck(t, started)
	if first+second != 1 {
		t.Fatal("the first two files run independently", first, second)
	}
	start(2)
	start(3)
	for range documentCheckCapacity - len(keys) {
		if !q.reserve() {
			t.Fatal("capacity was lost before the queue filled")
		}
	}
	if q.reserve() {
		t.Fatal("the queue accepted more than its capacity")
	}
	for range documentCheckCapacity - len(keys) {
		q.release()
	}
	select {
	case i := <-started:
		t.Fatal("queued work exceeded the worker limit", i)
	default:
	}
	q.mu.Lock()
	lastA, lastB := q.tails["a"].done, q.tails["b"].done
	q.mu.Unlock()
	close(release[0])
	if next := waitDocumentCheck(t, started); next != 2 {
		t.Fatal("confirmations must follow the preceding check", next)
	}
	close(release[2])
	if next := waitDocumentCheck(t, started); next != 3 {
		t.Fatal("newer confirmations must retain their order", next)
	}
	close(release[3])
	close(release[1])
	waitDocumentCheck(t, lastA)
	waitDocumentCheck(t, lastB)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending != 0 || len(q.workers) != 0 || len(q.tails) != 0 {
		t.Fatal("finished work retained resources")
	}
}

func TestDocumentCheckCopiesShareWorkAndShutdownCancelsIt(t *testing.T) {
	q := newDocumentCheckQueue()
	t.Cleanup(q.cancel)
	started, cancelled := make(chan struct{}), make(chan struct{})
	parent, cancel := context.WithCancel(t.Context())
	cancel()
	key := documentCheckKey{document: "a"}
	if !q.reserve() || !q.start(parent, key, func(ctx context.Context) {
		if ctx.Err() != nil {
			t.Error("returning an upload cancelled its accepted check")
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
	}) {
		t.Fatal("check was refused")
	}
	waitDocumentCheck(t, started)
	if !q.reserve() || !q.start(t.Context(), key, func(context.Context) { t.Error("an identical in-flight check ran twice") }) {
		t.Fatal("copy was refused")
	}
	if q.waiting() != 1 {
		t.Fatal("the copy retained a slot")
	}
	q.mu.Lock()
	done := q.tails["a"].done
	q.mu.Unlock()
	q.cancel()
	waitDocumentCheck(t, cancelled)
	waitDocumentCheck(t, done)
	if q.reserve() || q.waiting() != 0 {
		t.Fatal("shutdown retained or accepted work")
	}
}

func TestDocumentCheckQueueKeepsChangesBetweenIdenticalReadings(t *testing.T) {
	q := newDocumentCheckQueue()
	t.Cleanup(q.cancel)
	started := make(chan byte, 3)
	release := make(chan struct{})
	start := func(reading byte) {
		t.Helper()
		key := documentCheckKey{document: "a", reading: [32]byte{reading}}
		if !q.reserve() || !q.start(t.Context(), key, func(ctx context.Context) {
			started <- reading
			select {
			case <-release:
			case <-ctx.Done():
			}
		}) {
			t.Fatal("confirmation was refused")
		}
	}
	start(0)
	if first := waitDocumentCheck(t, started); first != 0 {
		t.Fatal("the first reading did not start")
	}
	start(1)
	start(0)
	if q.waiting() != 3 {
		t.Fatal("a later repetition must run after the intervening change")
	}
	close(release)
	if next := waitDocumentCheck(t, started); next != 1 {
		t.Fatal("the intervening reading was lost", next)
	}
	if last := waitDocumentCheck(t, started); last != 0 {
		t.Fatal("the final reading was lost", last)
	}
}

func TestDocumentCheckQueueAcceptsEveryAddedFile(t *testing.T) {
	q := newDocumentCheckQueue()
	t.Cleanup(q.cancel)
	const files = 120
	gate := make(chan struct{})
	finished := make(chan int, files)
	var mu sync.Mutex
	running, most := 0, 0
	for i := range files {
		if !q.admit() {
			t.Fatal("an added file's check was refused while checks were busy", i)
		}
		key := documentCheckKey{document: fmt.Sprintf("file-%d", i)}
		if !q.start(t.Context(), key, func(ctx context.Context) {
			mu.Lock()
			running++
			most = max(most, running)
			mu.Unlock()
			select {
			case <-gate:
			case <-ctx.Done():
			}
			mu.Lock()
			running--
			mu.Unlock()
			finished <- i
		}) {
			t.Fatal("an added file's check was not started", i)
		}
	}
	if q.waiting() != files {
		t.Fatal("every added file waits its turn", q.waiting())
	}
	if q.reserve() {
		t.Fatal("asking to check again is refused while the queue is full, so the person can retry")
	}
	close(gate)
	for range files {
		waitDocumentCheck(t, finished)
	}
	if most > documentCheckWorkers {
		t.Fatal("more checks ran at once than the workers allow", most)
	}
	deadline := time.Now().Add(5 * time.Second)
	for q.waiting() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if q.waiting() != 0 {
		t.Fatal("finished checks left slots taken", q.waiting())
	}
}
