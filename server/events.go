package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vult-ai/vult/ledger"
)

// Live updates: the app keeps one stream open per Book and reloads what
// changed. The database announces which Book changed and in what area
// (entries, proposals or bank activity); no financial data travels in the
// announcement or the stream.

const eventsPath = "/api/v1/events"

// maxEventStreams bounds open streams for the whole server.
const maxEventStreams = 64

type bookEvents struct {
	mu      sync.Mutex
	streams map[string]map[chan string]struct{} // tenant/book -> streams
	open    int
	// closing ends every stream when the server shuts down, so shutdown
	// doesn't wait on connections that never go idle.
	closing chan struct{}
	once    sync.Once
	// db is where announcements come from. The listener runs only while a
	// stream is open, on its own connection outside the pool.
	db   *pgxpool.Pool
	stop context.CancelFunc
}

func newBookEvents(db *pgxpool.Pool) *bookEvents {
	return &bookEvents{streams: map[string]map[chan string]struct{}{}, closing: make(chan struct{}), db: db}
}

func (e *bookEvents) close() { e.once.Do(func() { close(e.closing) }) }

func (e *bookEvents) subscribe(tenant, book string) (chan string, func(), bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.open >= maxEventStreams {
		return nil, nil, false
	}
	key := tenant + "/" + book
	ch := make(chan string, 8)
	if e.streams[key] == nil {
		e.streams[key] = map[chan string]struct{}{}
	}
	e.streams[key][ch] = struct{}{}
	if e.open == 0 && e.db != nil {
		ctx, stop := context.WithCancel(context.Background())
		e.stop = stop
		go e.listen(ctx, e.db)
	}
	e.open++
	return ch, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.streams[key], ch)
		if len(e.streams[key]) == 0 {
			delete(e.streams, key)
		}
		e.open--
		if e.open == 0 && e.stop != nil {
			e.stop()
			e.stop = nil
		}
	}, true
}

// Publish tells every stream for a Book that an area changed. A slow stream
// misses a duplicate notice rather than blocking the others.
func (e *bookEvents) Publish(tenant, book, area string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for ch := range e.streams[tenant+"/"+book] {
		select {
		case ch <- area:
		default:
		}
	}
}

// listen relays the database's announcements until ctx ends, reconnecting
// after a lost connection.
func (e *bookEvents) listen(ctx context.Context, db *pgxpool.Pool) {
	for ctx.Err() == nil {
		conn, err := pgx.ConnectConfig(ctx, db.Config().ConnConfig.Copy())
		if err == nil {
			if _, err = conn.Exec(ctx, "LISTEN vult_book_changes"); err == nil {
				for {
					n, err := conn.WaitForNotification(ctx)
					if err != nil {
						break
					}
					parts := strings.Fields(n.Payload)
					if len(parts) == 3 && ledger.ValidID(parts[0]) && ledger.ValidID(parts[1]) {
						e.Publish(parts[0], parts[1], parts[2])
					}
				}
			}
			conn.Close(context.Background())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// events streams change notices for one Book the signed-in owner owns. The
// stream ends when the session does, rechecked every minute.
func (a *webAPI) events(w http.ResponseWriter, r *http.Request, session string) {
	q := r.URL.Query()
	book := q.Get("book_id")
	if a.bookEvents == nil || len(q) != 1 || !ledger.ValidID(book) {
		failure(w, http.StatusNotFound)
		return
	}
	tenant, err := a.sessions.BookTenant(r.Context(), session, book)
	if err != nil {
		apiFailure(w, err)
		return
	}
	ch, done, ok := a.bookEvents.subscribe(tenant, book)
	if !ok {
		w.Header().Set("Retry-After", "30")
		failure(w, http.StatusServiceUnavailable)
		return
	}
	defer done()
	control := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	write := func(format string, args ...any) bool {
		_ = control.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return false
		}
		return control.Flush() == nil
	}
	if !write("retry: 5000\n\n") {
		return
	}
	check := time.NewTicker(time.Minute)
	defer check.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-a.bookEvents.closing:
			return
		case area := <-ch:
			if !write("event: changed\ndata: %s\n\n", area) {
				return
			}
		case <-check.C:
			if _, err := a.sessions.BookTenant(context.WithoutCancel(r.Context()), session, book); err != nil {
				return
			}
			if !write(": still here\n\n") {
				return
			}
		}
	}
}
