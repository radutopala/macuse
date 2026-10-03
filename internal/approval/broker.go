package approval

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/radutopala/mac-use/internal/proto"
)

// Answers to a request.
type Answer string

const (
	// AnswerDeny refuses this request only.
	AnswerDeny Answer = "deny"
	// AnswerSession allows the app for the asking session.
	AnswerSession Answer = "session"
	// AnswerAlways saves an allow for the app.
	AnswerAlways Answer = "always"
)

// Valid reports whether a is one of the answers.
func (a Answer) Valid() bool {
	return a == AnswerDeny || a == AnswerSession || a == AnswerAlways
}

// ErrTimeout means the user didn't answer in time.
var ErrTimeout = errors.New("the user didn't answer")

// ErrUnknown means no request has that id, or it was already answered.
var ErrUnknown = errors.New("no such request")

// Request asks the user to let a client control an app.
type Request struct {
	ID        string    `json:"id"`
	Client    string    `json:"client"`
	Session   string    `json:"session"`
	App       proto.App `json:"app"`
	Action    string    `json:"action"`
	CreatedAt time.Time `json:"created_at"`
}

type pending struct {
	req     Request
	done    chan struct{}
	answer  Answer
	waiters int
}

// Broker holds the requests waiting on the user. A session asking again
// for an app it already waits on joins the same request.
type Broker struct {
	timeout  time.Duration
	now      func() time.Time
	onChange func()

	mu      sync.Mutex
	pending map[string]*pending
	order   []string
	next    int
}

// NewBroker returns a Broker whose requests wait up to timeout. onChange,
// if set, runs (without the lock) whenever the pending list changes.
func NewBroker(timeout time.Duration, now func() time.Time, onChange func()) *Broker {
	return &Broker{timeout: timeout, now: now, onChange: onChange, pending: map[string]*pending{}}
}

// Ask waits for the user's answer to req (its ID and CreatedAt are set
// here). It returns ErrTimeout when no answer comes in time, and the
// context's error when ctx ends first.
func (b *Broker) Ask(ctx context.Context, req Request) (Answer, error) {
	p, added := b.join(req)
	if added {
		b.changed()
	}
	timer := time.NewTimer(b.timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.answer, nil
	case <-timer.C:
		b.leave(p)
		return AnswerDeny, ErrTimeout
	case <-ctx.Done():
		b.leave(p)
		return AnswerDeny, ctx.Err()
	}
}

func (b *Broker) join(req Request) (*pending, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.order {
		p := b.pending[id]
		if p.req.Session == req.Session && p.req.App.BundleID == req.App.BundleID && p.req.App.TeamID == req.App.TeamID {
			p.waiters++
			return p, false
		}
	}
	b.next++
	req.ID = strconv.Itoa(b.next)
	req.CreatedAt = b.now().UTC()
	p := &pending{req: req, done: make(chan struct{}), waiters: 1}
	b.pending[req.ID] = p
	b.order = append(b.order, req.ID)
	return p, true
}

// leave drops a waiter that stopped waiting; the last one takes the
// request off the list.
func (b *Broker) leave(p *pending) {
	b.mu.Lock()
	p.waiters--
	removed := p.waiters == 0 && b.remove(p.req.ID)
	b.mu.Unlock()
	if removed {
		b.changed()
	}
}

// remove takes the request off the list, reporting whether it was there.
// The caller holds the lock.
func (b *Broker) remove(id string) bool {
	if _, ok := b.pending[id]; !ok {
		return false
	}
	delete(b.pending, id)
	for i, o := range b.order {
		if o == id {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	return true
}

// Answer answers the request with id, releasing everyone waiting on it.
func (b *Broker) Answer(id string, a Answer) (Request, error) {
	b.mu.Lock()
	p, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return Request{}, ErrUnknown
	}
	b.remove(id)
	p.answer = a
	close(p.done)
	b.mu.Unlock()
	b.changed()
	return p.req, nil
}

// Pending lists the waiting requests, oldest first.
func (b *Broker) Pending() []Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Request, 0, len(b.order))
	for _, id := range b.order {
		out = append(out, b.pending[id].req)
	}
	return out
}

func (b *Broker) changed() {
	if b.onChange != nil {
		b.onChange()
	}
}
