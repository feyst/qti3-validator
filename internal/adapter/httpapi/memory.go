package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"qti3-validator/internal/app"
)

// memSem is the memory the validations in progress may reserve together
// (MaxValidationMemory), in bytes. A validation reserves what its document
// takes before it takes it (app.Memory), and one that finds no room waits
// until others release theirs.
//
// A validation that waits is parked: it keeps what it reserved, which it
// really holds, but runs nothing. Reservations are granted when they fit;
// when nothing fits and every holder is parked, nobody would ever release,
// so one of them is granted its reservation all the same. That exceeds the
// budget by no more than what the parked ones hold, which is their
// documents as read and no trees, and it cannot deadlock.
type memSem struct {
	mu     sync.Mutex
	budget int64
	used   int64 // reserved by all validations
	parked int64 // of used: reserved by the validations that wait
	// urgent counts the waiters that have waited so long that no
	// reservation is granted before theirs, so a stream of small documents
	// cannot starve a large one for ever.
	urgent int
	// changed is closed and replaced whenever a waiter may have got room.
	changed chan struct{}
}

// urgentAfter is how long a validation waits for memory before those that
// came after it must wait for it.
const urgentAfter = 2 * time.Second

func newMemSem(budget int64) *memSem {
	if budget <= 0 {
		return nil
	}
	return &memSem{budget: budget, changed: make(chan struct{})}
}

func (m *memSem) wake() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// resize changes a reservation of a running validation from cur to n bytes,
// if n is less or fits. A validation that is the only one running gets it
// when the others, parked, leave no room: see memSem.
func (m *memSem) resize(cur, n int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case n <= cur:
		m.used -= cur - n
		m.wake()
		return true
	case m.urgent > 0:
		return false
	case m.used-cur+n <= m.budget || m.used-m.parked-cur == 0:
		m.used += n - cur
		return true
	}
	return false
}

// park marks cur bytes as held by a validation that waits, which lets
// another one in when it was the last one running.
func (m *memSem) park(cur int64) {
	m.mu.Lock()
	m.parked += cur
	m.wake()
	m.mu.Unlock()
}

// admit grants a parked validation its reservation of n bytes instead of
// cur, if it fits or nobody is running. It returns the channel to wait on
// otherwise. urgent tells whether this waiter is one of the urgent ones;
// the others wait for those.
func (m *memSem) admit(cur, n int64, urgent bool) (bool, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.urgent > 0 && !urgent {
		return false, m.changed
	}
	if m.used-cur+n <= m.budget || m.used-m.parked == 0 {
		m.parked -= cur
		m.used += n - cur
		if urgent {
			m.urgent--
			m.wake() // those that waited for this one
		}
		return true, nil
	}
	return false, m.changed
}

// unpark takes a waiter that gives up out of the parked ones; it keeps its
// cur bytes.
func (m *memSem) unpark(cur int64, urgent bool) {
	m.mu.Lock()
	m.parked -= cur
	if urgent {
		m.urgent--
	}
	m.wake()
	m.mu.Unlock()
}

func (m *memSem) setUrgent() {
	m.mu.Lock()
	m.urgent++
	m.mu.Unlock()
}

// memoryHold is what one request holds while it is validated: its
// validation slot, and its reservation in MaxValidationMemory, which the
// validation changes through app.Memory. It belongs to the request's
// goroutine.
type memoryHold struct {
	s           *Server
	held        int64  // bytes reserved
	releaseSlot func() // nil while the request holds no slot
	// busy is why the request found no room in time, once it did not: it is
	// then answered with 503, whatever the validation made of it.
	busy string
}

var _ app.Memory = (*memoryHold)(nil)

// newMemoryHold returns the hold of a request that has its slot.
func (s *Server) newMemoryHold(releaseSlot func()) *memoryHold {
	return &memoryHold{s: s, releaseSlot: releaseSlot}
}

// context returns ctx with the hold as the memory of the validation, when
// the server bounds memory.
func (h *memoryHold) context(ctx context.Context) context.Context {
	if h.s.mem == nil {
		return ctx
	}
	return app.WithMemory(ctx, h)
}

// release frees the reservation and the slot.
func (h *memoryHold) release() {
	if h.s.mem != nil && h.held > 0 {
		h.s.mem.resize(h.held, 0)
		h.held = 0
	}
	if h.releaseSlot != nil {
		h.releaseSlot()
		h.releaseSlot = nil
	}
}

// refused answers 503 when the validation stopped because it found no room
// in memory in time, and reports whether it did: such a request is to be
// tried again, not reported on.
func (h *memoryHold) refused(w http.ResponseWriter) bool {
	if h.busy == "" {
		return false
	}
	writeBusy(w, h.busy)
	return true
}

// Limit implements app.Memory.
func (h *memoryHold) Limit() int64 { return h.s.mem.budget }

// Reserve implements app.Memory. A reservation that does not fit waits for
// room like a request waits for a slot: at most MaxQueue requests wait, and
// until ctx is done. While it waits the request gives up its slot, so the
// validations that do fit, small ones mostly, go on beside the ones that
// run; it takes a slot again once it has its memory.
func (h *memoryHold) Reserve(ctx context.Context, n int64) error {
	m := h.s.mem
	switch {
	case n > h.held && h.busy != "":
		return app.ErrMemoryBusy // it gave up before; what follows is cleaning up
	case n > m.budget:
		return app.ErrMemoryExceeded
	case m.resize(h.held, n):
		h.held = n
		return nil
	}
	s := h.s
	if s.waiting.Add(1) > int64(s.opts.MaxQueue) {
		s.waiting.Add(-1)
		h.busy = "too many requests are waiting for memory to validate in (MAX_VALIDATION_MEMORY)"
		return app.ErrMemoryBusy
	}
	defer s.waiting.Add(-1)
	if h.releaseSlot != nil {
		h.releaseSlot()
		h.releaseSlot = nil
	}
	m.park(h.held)
	urgent := false
	timer := time.NewTimer(urgentAfter)
	defer timer.Stop()
	for {
		ok, changed := m.admit(h.held, n, urgent)
		if ok {
			h.held = n
			break
		}
		select {
		case <-changed:
		case <-timer.C:
			urgent = true
			m.setUrgent()
		case <-ctx.Done():
			m.unpark(h.held, urgent)
			h.busy = "no memory to validate in came free in time (MAX_VALIDATION_MEMORY)"
			return app.ErrMemoryBusy
		}
	}
	select {
	case s.slots <- struct{}{}:
		h.releaseSlot = func() { <-s.slots }
		return nil
	case <-ctx.Done():
		h.busy = "no validation slot available in time"
		return app.ErrMemoryBusy
	}
}
