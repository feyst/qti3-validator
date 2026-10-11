package httpapi

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"qti3-validator/internal/app"
)

// memSem is the memory the validations in progress may reserve together
// (MaxValidationMemory), in bytes. A validation reserves what its document
// takes before it takes it (app.Memory), and one that finds no room waits
// until others release theirs.
//
// What is reserved never exceeds the budget. A validation that waits has
// reserved nothing, and holds nothing in memory: the application lets go of
// the document it read and reads it again afterwards. So the validations
// that wait cost no memory, however many they are; they cannot wait for
// each other, as none of them holds what another needs; and each is served
// once the ones that run release enough, as none asks for more than the
// whole budget.
type memSem struct {
	mu     sync.Mutex
	budget int64
	used   int64 // reserved by the validations that run
	// urgent holds the waiters that have waited so long that no reservation
	// is granted before theirs, the oldest first, which alone is served: a
	// stream of small documents cannot starve a large one, nor the large
	// ones each other.
	urgent []uint64
	seq    uint64 // the last waiter's number
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

// resize changes the reservation of a running validation from cur to n
// bytes, when n is less or fits, and reports whether it did. Nothing more is
// granted while a waiter is urgent.
func (m *memSem) resize(cur, n int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case n <= cur:
		m.used -= cur - n
		m.wake()
		return true
	case len(m.urgent) == 0 && m.used-cur+n <= m.budget:
		m.used += n - cur
		return true
	}
	return false
}

// enqueue numbers a validation that starts to wait.
func (m *memSem) enqueue() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	return m.seq
}

// admit grants waiter id, which holds nothing, a reservation of n bytes if
// it fits and no urgent waiter goes first. It returns the channel to wait
// on otherwise.
func (m *memSem) admit(id uint64, n int64) (bool, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.urgent) > 0 && m.urgent[0] != id || m.used+n > m.budget {
		return false, m.changed
	}
	m.used += n
	m.forget(id)
	return true, nil
}

// setUrgent puts waiter id behind the urgent ones.
func (m *memSem) setUrgent(id uint64) {
	m.mu.Lock()
	m.urgent = append(m.urgent, id)
	m.mu.Unlock()
}

// leave takes a waiter that gives up out of the urgent ones.
func (m *memSem) leave(id uint64) {
	m.mu.Lock()
	m.forget(id)
	m.mu.Unlock()
}

// forget removes id from the urgent waiters, and wakes the others when the
// first of them changed. The caller holds mu.
func (m *memSem) forget(id uint64) {
	if i := slices.Index(m.urgent, id); i >= 0 {
		m.urgent = slices.Delete(m.urgent, i, i+1)
		m.wake()
	}
}

// memoryHold is what one request holds while it is validated: its
// validation slot, and its reservation in MaxValidationMemory, which the
// validation changes through app.Memory. It belongs to the request's
// goroutine.
type memoryHold struct {
	s     *Server
	held  int64 // bytes reserved
	slot  bool  // the request holds its slot
	large bool  // the kind of slot it takes; see Server.takeSlot
	// busy is why the request found no room in time, once it did not: it is
	// then answered with 503, whatever the validation made of it.
	busy string
}

var _ app.Memory = (*memoryHold)(nil)

// newMemoryHold returns the hold of a request that has its slot.
func (s *Server) newMemoryHold(large bool) *memoryHold {
	return &memoryHold{s: s, slot: true, large: large}
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
	h.TryReserve(0)
	if h.slot {
		h.s.freeSlot(h.large)
		h.slot = false
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

// TryReserve implements app.Memory.
func (h *memoryHold) TryReserve(n int64) bool {
	m := h.s.mem
	if m == nil || n == h.held {
		return true
	}
	if n > h.held && h.busy != "" || !m.resize(h.held, n) {
		return false
	}
	h.held = n
	return true
}

// Reserve implements app.Memory. A reservation that does not fit waits for
// room like a request waits for a slot: at most MaxQueue requests wait, and
// until ctx is done; a request that would be one too many, or runs out of
// time, is answered with 503, also when part of its validation is done.
// While it waits the request gives up what it reserved and its slot, so the
// validations that do fit, small ones mostly, go on beside the ones that
// run; it takes a slot again once it has its memory.
func (h *memoryHold) Reserve(ctx context.Context, n int64) error {
	m := h.s.mem
	if n > m.budget {
		h.TryReserve(0)
		return app.ErrMemoryExceeded
	}
	if h.TryReserve(n) {
		return nil
	}
	h.TryReserve(0)
	if h.busy != "" {
		return app.ErrMemoryBusy // it gave up before
	}
	s := h.s
	if s.waiting.Add(1) > int64(s.opts.MaxQueue) {
		s.waiting.Add(-1)
		h.busy = "too many requests are waiting for memory to validate in (MAX_VALIDATION_MEMORY)"
		return app.ErrMemoryBusy
	}
	defer s.waiting.Add(-1)
	if h.slot {
		s.freeSlot(h.large)
		h.slot = false
	}
	id := m.enqueue()
	timer := time.NewTimer(urgentAfter)
	defer timer.Stop()
	for {
		ok, changed := m.admit(id, n)
		if ok {
			h.held = n
			break
		}
		select {
		case <-changed:
		case <-timer.C:
			m.setUrgent(id)
		case <-ctx.Done():
			m.leave(id)
			h.busy = "no memory to validate in came free in time (MAX_VALIDATION_MEMORY)"
			return app.ErrMemoryBusy
		}
	}
	if !s.takeSlot(ctx, h.large) {
		h.TryReserve(0)
		h.busy = "no validation slot available in time"
		return app.ErrMemoryBusy
	}
	h.slot = true
	return nil
}
