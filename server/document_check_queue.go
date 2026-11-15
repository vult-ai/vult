package server

import (
	"context"
	"sync"
)

// documentCheckCapacity bounds checks the person asks for again. Checks for
// newly added files always wait their turn: an added file is never refused
// because checks are busy. Waiting jobs hold metadata only; a file's bytes are
// read when its turn comes.
const documentCheckCapacity = 50
const documentCheckWorkers = 2

type documentCheckKey struct {
	document         string
	session, reading [32]byte
}

type documentCheckTail struct {
	key  documentCheckKey
	done chan struct{}
}

// documentCheckQueue bounds accepted work before any document bytes are read.
// Jobs for one document run in acceptance order, including newer confirmations.
type documentCheckQueue struct {
	mu      sync.Mutex
	pending int
	workers chan struct{}
	tails   map[string]documentCheckTail
	ctx     context.Context
	cancel  context.CancelFunc
}

func newDocumentCheckQueue() *documentCheckQueue {
	ctx, cancel := context.WithCancel(context.Background())
	return &documentCheckQueue{workers: make(chan struct{}, documentCheckWorkers),
		tails: map[string]documentCheckTail{}, ctx: ctx, cancel: cancel}
}

// reserve takes a slot for a check the person asks for again, refusing when
// the queue is full so they can try again shortly.
func (q *documentCheckQueue) reserve() bool { return q.take(true) }

// admit takes a slot for a newly added file. It refuses only while Vult is
// shutting down; otherwise the check waits its turn.
func (q *documentCheckQueue) admit() bool { return q.take(false) }

func (q *documentCheckQueue) take(bounded bool) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.ctx.Err() != nil || bounded && q.pending >= documentCheckCapacity {
		return false
	}
	q.pending++
	return true
}

func (q *documentCheckQueue) release() {
	q.mu.Lock()
	q.pending--
	q.mu.Unlock()
}

// waiting is the number of accepted checks not yet finished.
func (q *documentCheckQueue) waiting() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.pending
}

// start takes the reserved slot only when it accepts the job or finds its copy.
func (q *documentCheckQueue) start(parent context.Context, key documentCheckKey, run func(context.Context)) bool {
	q.mu.Lock()
	if q.ctx.Err() != nil {
		q.mu.Unlock()
		return false
	}
	previous := q.tails[key.document]
	if previous.done != nil && previous.key == key {
		q.mu.Unlock()
		q.release()
		return true
	}
	done := make(chan struct{})
	q.tails[key.document] = documentCheckTail{key: key, done: done}
	q.mu.Unlock()
	go func() {
		defer func() {
			q.mu.Lock()
			if q.tails[key.document].done == done {
				delete(q.tails, key.document)
			}
			q.pending-- // the lock is already held
			close(done)
			q.mu.Unlock()
		}()
		ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
		stop := context.AfterFunc(q.ctx, cancel)
		defer stop()
		defer cancel()
		if previous.done != nil {
			select {
			case <-previous.done:
			case <-ctx.Done():
				return
			}
		}
		select {
		case q.workers <- struct{}{}:
			defer func() { <-q.workers }()
		case <-ctx.Done():
			return
		}
		if ctx.Err() != nil {
			return
		}
		ctx, timeout := context.WithTimeout(ctx, checkerTime)
		defer timeout()
		run(ctx)
	}()
	return true
}
