package httpapi

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"qti3-validator/internal/app"
)

// smallReservation is the most a validation may reserve and still be small:
// what a typical item takes, with room to spare. A document under
// smallRequestSize is read within it.
const smallReservation = 4 << 20

// SmallMemoryShare is the part of a budget of MaxValidationMemory that is
// set apart for small validations, with maxConcurrent slots: room for one
// on every slot, and at most an eighth of the budget. The rest is what one
// document may take.
func SmallMemoryShare(budget int64, maxConcurrent int) int64 {
	return min(budget/8, int64(max(maxConcurrent, 1))*smallReservation)
}

// memSem is the memory the validations in progress may reserve together
// (MaxValidationMemory), in bytes. A validation reserves what its document
// takes before it takes it (app.Memory), and one that finds no room waits
// until others release theirs.
//
// The budget has two parts. Reservations of up to smallReservation come
// from a share of their own (SmallMemoryShare) and are granted whenever
// they fit there: typical items go on, whatever waits for the rest. The
// rest is for the large reservations, in which those that waited long are
// served first; the small ones never take from it, so they cannot starve a
// large one either.
//
// What is reserved never exceeds the budget. A validation that waits has
// reserved nothing, and holds nothing in memory: the application lets go of
// the document it read and reads it again afterwards. So the validations
// that wait cost no memory, however many they are; they cannot wait for
// each other, as none of them holds what another needs; and each is served
// once the ones that run release enough, as none asks for more than its
// part of the budget.
type memSem struct {
	mu        sync.Mutex
	large     int64 // the part for large reservations
	small     int64 // the part for small ones
	usedLarge int64
	usedSmall int64
	// urgent holds the numbers of the waiters for a large reservation that
	// have waited so long that nothing large is granted before theirs, in
	// the order they came; the first alone is served. A stream of documents
	// that fit cannot starve one that needs most of the budget, nor can
	// those starve each other.
	urgent []uint64
	seq    uint64 // the last waiter's number
	// changed is closed and replaced whenever a waiter may have got room.
	changed chan struct{}
}

// urgentAfter is how long a validation waits for a large reservation before
// those that came after it must wait for it.
const urgentAfter = 2 * time.Second

func newMemSem(budget int64, maxConcurrent int) *memSem {
	if budget <= 0 {
		return nil
	}
	small := SmallMemoryShare(budget, maxConcurrent)
	return &memSem{large: budget - small, small: small, changed: make(chan struct{})}
}

func (m *memSem) wake() {
	close(m.changed)
	m.changed = make(chan struct{})
}

// reservation is what a validation has reserved, and from which part.
type reservation struct {
	n     int64
	small bool
}

func isSmall(n int64) bool { return n <= smallReservation }

// resize changes the reservation of a running validation to n bytes, when n
// is less or fits, and reports whether it did. A reservation that shrinks
// stays in its part; one that grows beyond smallReservation moves to the
// large part. Nothing large is granted while a waiter is urgent.
func (m *memSem) resize(r *reservation, n int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case n <= r.n:
		if r.small {
			m.usedSmall -= r.n - n
		} else {
			m.usedLarge -= r.n - n
		}
		r.n = n
		m.wake()
		return true
	case isSmall(n) && (r.small || r.n == 0):
		if m.usedSmall-r.n+n > m.small {
			return false
		}
		m.usedSmall += n - r.n
		r.n, r.small = n, true
		return true
	case len(m.urgent) > 0:
		return false
	case r.small:
		if m.usedLarge+n > m.large {
			return false
		}
		m.usedSmall -= r.n
		m.usedLarge += n
		r.n, r.small = n, false
		m.wake()
		return true
	case m.usedLarge-r.n+n <= m.large:
		m.usedLarge += n - r.n
		r.n = n
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
// it fits in its part and, for a large one, no urgent waiter goes first. It
// returns the channel to wait on otherwise.
func (m *memSem) admit(id uint64, r *reservation, n int64) (bool, <-chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if isSmall(n) {
		if m.usedSmall+n > m.small {
			return false, m.changed
		}
		m.usedSmall += n
		r.n, r.small = n, true
		return true, nil
	}
	if len(m.urgent) > 0 && m.urgent[0] != id || m.usedLarge+n > m.large {
		return false, m.changed
	}
	m.usedLarge += n
	r.n, r.small = n, false
	m.forget(id)
	return true, nil
}

// setUrgent puts waiter id among the urgent ones, which are in the order of
// their numbers: the order they first waited in.
func (m *memSem) setUrgent(id uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	i, found := slices.BinarySearch(m.urgent, id)
	if !found {
		m.urgent = slices.Insert(m.urgent, i, id)
		if i == 0 {
			m.wake() // it goes first now
		}
	}
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
//
// A request is small when it is one document with a body under
// smallRequestSize and its validation reserves no more than
// smallReservation: it takes any slot, the express one too, and its memory
// from the small share. A package is a large request from the start: it
// takes a regular slot only, while each of its documents takes its memory
// from the small share or from the rest, by what it reserves. A small request
// whose document turns out to need more, one of many nodes in few bytes,
// becomes a large one when it reserves that: it then needs a regular slot,
// and room in the large part. So what runs on the express slot, beside the
// MaxConcurrent validations on the regular ones, holds memory from the
// small share only.
type memoryHold struct {
	s     *Server
	held  reservation
	slot  slotKind // the slot the request holds, if any
	large bool     // the kind of slot it takes; see Server.takeSlot
	// id is the number of the request among those that wait for a large
	// reservation, and since when it first did, once it has: a request that
	// waits a second time keeps its place.
	id    uint64
	since time.Time
	// busy is why the request found no room in time, once it did not: it is
	// then answered with 503, whatever the validation made of it.
	busy string
}

var _ app.Memory = (*memoryHold)(nil)

// newMemoryHold returns the hold of a request that has its slot.
func (s *Server) newMemoryHold(slot slotKind, large bool) *memoryHold {
	return &memoryHold{s: s, slot: slot, large: large}
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
	h.freeSlot()
}

// freeSlot gives the slot back, if the request holds one.
func (h *memoryHold) freeSlot() {
	h.s.freeSlot(h.slot)
	h.slot = noSlot
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

// Limit implements app.Memory: the large part of the budget.
func (h *memoryHold) Limit() int64 { return h.s.mem.large }

// TryReserve implements app.Memory.
func (h *memoryHold) TryReserve(n int64) bool {
	m := h.s.mem
	if m == nil || n == h.held.n {
		return true
	}
	if n < h.held.n {
		return m.resize(&h.held, n)
	}
	if h.busy != "" {
		return false
	}
	// A small request that grows large gives up the express slot for a
	// regular one, if one is free; it gets the memory only with that slot.
	swap := !isSmall(n) && h.slot == expressSlot
	if swap && !h.s.tryTakeRegular() {
		return false
	}
	if !m.resize(&h.held, n) {
		if swap {
			h.s.freeSlot(regularSlot)
		}
		return false
	}
	if swap {
		h.s.freeSlot(expressSlot)
		h.slot = regularSlot
	}
	h.large = h.large || !isSmall(n)
	return true
}

// waitsAsLarge reports whether a wait for n bytes counts among the large
// requests. A waiter counts by the kind of its request, not only of this
// reservation: an entry of a package that reserves little waits as long as a
// large document does, and would fill the queue of the typical items.
func (h *memoryHold) waitsAsLarge(n int64) bool {
	return h.large || !isSmall(n)
}

// Reserve implements app.Memory. A reservation that does not fit waits for
// room like a request waits for a slot: at most MaxQueue requests wait for
// a large reservation, and as many for a small one, until ctx is done; a
// request that would be one too many, or runs out of time, is answered with
// 503, also when part of its validation is done. While it waits the request
// gives up what it reserved and its slot, so the validations that do fit go
// on beside the ones that run; it takes a slot again once it has its
// memory. What is reserved from the small share is held only by validations
// that have a slot.
func (h *memoryHold) Reserve(ctx context.Context, n int64) error {
	m := h.s.mem
	if n > m.large {
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
	large := !isSmall(n)
	waiting := s.queue(h.waitsAsLarge(n))
	if waiting.Add(1) > int64(s.opts.MaxQueue) {
		waiting.Add(-1)
		h.busy = "too many requests are waiting for memory to validate in (MAX_VALIDATION_MEMORY)"
		return app.ErrMemoryBusy
	}
	defer waiting.Add(-1)
	h.freeSlot()
	// From here on the request is of the kind of its reservation.
	h.large = h.waitsAsLarge(n)
	if h.id == 0 {
		h.id, h.since = m.enqueue(), time.Now()
	}
	timer := time.NewTimer(max(urgentAfter-time.Since(h.since), 0))
	defer timer.Stop()
	for {
		ok, changed := m.admit(h.id, &h.held, n)
		if !ok {
			select {
			case <-changed:
			case <-timer.C:
				if large {
					m.setUrgent(h.id)
				}
			case <-ctx.Done():
				m.leave(h.id)
				h.busy = "no memory to validate in came free in time (MAX_VALIDATION_MEMORY)"
				return app.ErrMemoryBusy
			}
			continue
		}
		if h.slot = s.tryTakeSlot(h.large); h.slot != noSlot {
			return nil
		}
		// No slot is free. A large reservation is kept while the request
		// waits for one: it waited its turn for that room. A small one is
		// given back: the small share is for validations that run, and what
		// a request held of it while it waits for a regular slot, behind
		// large documents that take seconds, would keep typical items
		// waiting for memory that long.
		if !large {
			m.resize(&h.held, 0)
		}
		if h.slot = s.takeSlot(ctx, h.large); h.slot == noSlot {
			h.TryReserve(0)
			h.busy = "no validation slot available in time"
			return app.ErrMemoryBusy
		}
		if large || m.resize(&h.held, n) {
			return nil
		}
		// The room went to another in the meantime: wait for it again,
		// without the slot.
		h.freeSlot()
	}
}
