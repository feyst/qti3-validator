package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

func TestMemSem(t *testing.T) {
	if newMemSem(0) != nil {
		t.Fatal("no budget, no semaphore")
	}
	m := newMemSem(100)
	if !m.resize(0, 60) {
		t.Fatal("60 fits in 100")
	}
	if m.resize(0, 60) {
		t.Fatal("a second 60 does not fit beside the first")
	}
	if !m.resize(0, 40) || !m.resize(40, 10) || !m.resize(10, 40) {
		t.Fatal("40 fills the rest; less is always granted, and growing back fits")
	}
	if m.resize(60, 70) {
		t.Fatal("growing past the budget is never granted, to whoever asks")
	}
	// The one that holds 60 gives it up to wait for 70.
	m.resize(60, 0)
	id := m.enqueue()
	if ok, _ := m.admit(id, 70); ok {
		t.Fatal("70 does not fit beside 40")
	}
	m.resize(40, 0)
	if ok, _ := m.admit(id, 70); !ok {
		t.Fatal("room came free")
	}
	if m.used != 70 || len(m.urgent) != 0 {
		t.Fatalf("used %d, urgent %v", m.used, m.urgent)
	}
}

// A waiter that became urgent goes before the reservations that came later,
// and the urgent ones go in the order they became so: small documents
// cannot keep a large one out for ever, nor can large ones each other.
func TestMemSemUrgent(t *testing.T) {
	m := newMemSem(100)
	m.resize(0, 50) // a running validation
	first, second, late := m.enqueue(), m.enqueue(), m.enqueue()
	if ok, _ := m.admit(first, 90); ok {
		t.Fatal("90 does not fit beside 50")
	}
	m.setUrgent(first)
	m.setUrgent(second)
	if m.resize(0, 10) || m.resize(50, 60) {
		t.Fatal("nothing more is granted to those that run while a waiter is urgent, though it fits")
	}
	if ok, _ := m.admit(late, 10); ok {
		t.Fatal("a later waiter waits for the urgent ones")
	}
	if ok, _ := m.admit(second, 10); ok {
		t.Fatal("the second urgent waiter waits for the first, though it fits")
	}
	m.resize(50, 0)
	if ok, _ := m.admit(first, 90); !ok {
		t.Fatal("the first urgent waiter gets in once there is room")
	}
	if ok, _ := m.admit(late, 10); ok {
		t.Fatal("the later waiter still waits for the second urgent one")
	}
	if ok, _ := m.admit(second, 10); !ok {
		t.Fatal("the second urgent waiter is next")
	}
	m.setUrgent(late)
	m.leave(late)
	if !m.resize(0, 0) || len(m.urgent) != 0 || m.used != 100 {
		t.Fatalf("urgent %v, used %d", m.urgent, m.used)
	}
}

// reserving is a validator whose validations reserve need bytes, as the
// application does through its Memory, and then wait for release.
type reserving struct {
	need    int64
	release chan struct{}
	running atomic.Int64
	peak    atomic.Int64
}

func (r *reserving) run(ctx context.Context, size int64) qti.Outcome {
	need := r.need
	if size < 100 {
		need = 1 // a small document
	}
	if err := app.MemoryFrom(ctx).Reserve(ctx, need); err != nil {
		if errors.Is(err, app.ErrMemoryExceeded) {
			return qti.OutcomeTooLarge
		}
		return qti.OutcomeTooComplex
	}
	n := r.running.Add(1)
	for p := r.peak.Load(); n > p && !r.peak.CompareAndSwap(p, n); p = r.peak.Load() {
	}
	if size >= 100 {
		<-r.release
	}
	r.running.Add(-1)
	return qti.OutcomeValid
}

func (r *reserving) ValidateDocument(ctx context.Context, q app.ValidateDocument) qti.DocumentResult {
	o := r.run(ctx, q.Size)
	res := qti.DocumentResult{Valid: o == qti.OutcomeValid, Schema: "qti-assessment-item", Outcome: o}
	if o != qti.OutcomeValid {
		res.Errors = []qti.Finding{{Code: qti.CodeTooLarge, Message: "the document has 3.9 million nodes"}}
	}
	return res
}

func (r *reserving) ValidatePackage(ctx context.Context, q app.ValidatePackage) qti.PackageResult {
	o := r.run(ctx, q.Size)
	return qti.PackageResult{Valid: o == qti.OutcomeValid, Outcome: o}
}

var largeDoc = []byte("<x>" + strings.Repeat(" ", 200) + "</x>")

func postAsync(t *testing.T, url string, body []byte, n int) chan int {
	t.Helper()
	done := make(chan int, n)
	for range n {
		go func() { done <- postStatus(t, url, "application/xml", body) }()
	}
	return done
}

// idle reports whether the server holds nothing: no slot, no memory, nobody
// waiting.
func idle(s *Server) bool {
	if s.mem != nil {
		s.mem.mu.Lock()
		defer s.mem.mu.Unlock()
		if s.mem.used != 0 || len(s.mem.urgent) != 0 {
			return false
		}
	}
	return len(s.slots) == 0 && len(s.largeSlots) == 0 && s.waiting.Load() == 0
}

// Documents that together take more memory than the budget are validated
// one after the other, however many slots there are; the one that waits
// gives up its slot, so small documents are validated beside the one that
// runs.
func TestMemoryBoundSerialisesLargeDocuments(t *testing.T) {
	v := &reserving{need: 60, release: make(chan struct{})}
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2, MaxValidationMemory: 100,
	}, v)
	url := srv.URL + "/api/validate"
	done := postAsync(t, url, largeDoc, 3)
	waitFor(t, "one running, two waiting for memory", func() bool { return v.running.Load() == 1 && s.waiting.Load() == 2 })
	if len(s.slots) != 1 {
		t.Fatalf("%d slots taken: the waiting requests should hold none", len(s.slots))
	}
	for range 5 {
		if status := postStatus(t, url, "application/xml", []byte("<x/>")); status != http.StatusOK {
			t.Fatalf("a small document beside a large one and two waiting: %d", status)
		}
	}
	for range 3 {
		v.release <- struct{}{}
		if status := <-done; status != http.StatusOK {
			t.Fatalf("large document: %d", status)
		}
	}
	if v.peak.Load() > 2 {
		t.Fatalf("%d validations at once", v.peak.Load())
	}
	waitFor(t, "everything released", func() bool { return idle(s) })
}

// Documents that fit together are validated together.
func TestMemoryBoundAllowsWhatFits(t *testing.T) {
	v := &reserving{need: 30, release: make(chan struct{})}
	srv, _ := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 3, MaxValidationMemory: 100,
	}, v)
	done := postAsync(t, srv.URL+"/api/validate", largeDoc, 3)
	waitFor(t, "three running", func() bool { return v.running.Load() == 3 })
	close(v.release)
	for range 3 {
		if status := <-done; status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
	}
}

// A request that finds no memory before its deadline, or that would be one
// too many waiting, gets 503 with Retry-After, like one that finds no slot.
func TestMemoryBoundBusy(t *testing.T) {
	v := &reserving{need: 60, release: make(chan struct{})}
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2, MaxQueue: 1,
		MaxValidationMemory: 100, RequestTimeout: time.Second,
	}, v)
	url := srv.URL + "/api/validate"
	first := postAsync(t, url, largeDoc, 1)
	waitFor(t, "one running", func() bool { return v.running.Load() == 1 })
	second := make(chan response, 1)
	go func() { second <- postChunked(t, url, "application/xml", largeDoc) }()
	waitFor(t, "one waiting for memory", func() bool { return s.waiting.Load() == 1 })
	// The queue is full: at once.
	postBusy(t, url, "application/xml", largeDoc)
	// The one that waits runs out of time.
	got := <-second
	if got.status != http.StatusServiceUnavailable || firstCode(got.body) != codeBusy || got.header.Get("Retry-After") == "" {
		t.Fatalf("waited for memory until the deadline: %d %v", got.status, got.body)
	}
	close(v.release)
	<-first
	waitFor(t, "everything released", func() bool { return idle(s) })
}

// A document that needs more than the whole budget can never be validated:
// 413 with what the validator says of it, not 503.
func TestMemoryBoundExceeded(t *testing.T) {
	v := &reserving{need: 101, release: make(chan struct{})}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxValidationMemory: 100}, v)
	status, body := post(t, srv.URL+"/api/validate", "application/xml", largeDoc)
	msg, _ := body["message"].(string)
	if status != http.StatusRequestEntityTooLarge || firstCode(body) != string(qti.CodeTooLarge) || !strings.Contains(msg, "3.9 million nodes") {
		t.Fatalf("got %d %v", status, body)
	}
}

// Without a budget the validation has no Memory, and reserves nothing.
func TestMemoryBoundDisabled(t *testing.T) {
	var had atomic.Bool
	v := fakeValidator{wait: func(ctx context.Context) { had.Store(app.MemoryFrom(ctx) != nil) }}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20}, v)
	if status := postStatus(t, srv.URL+"/api/validate", "application/xml", []byte("<x/>")); status != http.StatusOK || had.Load() {
		t.Fatalf("status %d, memory in the context: %v", status, had.Load())
	}
}

// The service refuses a document by its nodes, not its bytes: with the real
// validator and a budget of 16 MiB, half a megabyte of prose is validated
// and half a megabyte of empty elements is refused with the numbers.
func TestMemoryBoundByNodes(t *testing.T) {
	srv := newTestServer(t, testOptions{MaxValidationMemory: 16 << 20})
	const open = `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false"><qti-item-body>`
	const closing = `</qti-item-body></qti-assessment-item>`
	prose := open + strings.Repeat(`<p>The quick brown fox jumps over the lazy dog, and then rests a while.</p>`, 7000) + closing
	dense := open + strings.Repeat(`<p/>`, len(prose)/4) + closing
	if status, body := post(t, srv.URL+"/api/validate", "application/xml", []byte(prose)); status != http.StatusOK || outcome(body) != "VALID" {
		t.Fatalf("prose of %d bytes: %d %v", len(prose), status, body)
	}
	status, body := post(t, srv.URL+"/api/validate", "application/xml", []byte(dense))
	msg, _ := body["message"].(string)
	if status != http.StatusRequestEntityTooLarge || firstCode(body) != string(qti.CodeTooLarge) {
		t.Fatalf("dense document of %d bytes: %d %v", len(dense), status, body)
	}
	for _, want := range []string{"thousand nodes", "allows 16 MiB", "MAX_VALIDATION_MEMORY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not say %q", msg, want)
		}
	}
}

// With two or more slots one is kept for small requests: large documents
// that take long do not keep a typical item waiting.
func TestSlotKeptForSmallRequests(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Int64
	v := fakeValidator{wait: func(context.Context) {
		running.Add(1)
		<-release
	}}
	srv, s := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2}, v)
	url := srv.URL + "/api/validate"
	large := []byte("<x>" + strings.Repeat(" ", smallRequestSize) + "</x>")
	done := postAsync(t, url, large, 2)
	waitFor(t, "one large document running and one waiting", func() bool { return running.Load() == 1 && s.waiting.Load() == 1 })
	small := postAsync(t, url, []byte("<x/>"), 1)
	waitFor(t, "the small document running beside it", func() bool { return running.Load() == 2 })
	if s.waiting.Load() != 1 || len(s.slots) != 2 {
		t.Fatalf("waiting %d, slots %d", s.waiting.Load(), len(s.slots))
	}
	close(release)
	for _, c := range []chan int{done, done, small} {
		if status := <-c; status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
	}
	waitFor(t, "everything released", func() bool { return idle(s) })

	// One slot is for whoever comes.
	one := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1}, v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if one.largeSlots != nil || !one.tryTakeSlot(true) || one.tryTakeSlot(false) {
		t.Fatal("with one slot a large request takes it")
	}
}

const mib = 1 << 20

func memServer(budget int64, concurrent, queue int) *Server {
	return New(Options{MaxPackageSize: 100 * mib, MaxConcurrent: concurrent, MaxQueue: queue, MaxValidationMemory: budget, RequestTimeout: time.Minute},
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// peakOf keeps the most of what it is shown.
type peakOf struct{ atomic.Int64 }

func (p *peakOf) see(v int64) {
	for old := p.Load(); v > old && !p.CompareAndSwap(old, v); old = p.Load() {
	}
}

func usedOf(m *memSem) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.used
}

// validateLike reserves through h as the application does for a document:
// to read it, then for its rules without waiting if there is room, and else
// with nothing while it waits. held is told what the validation really
// holds in memory: the document from when it is read until it is let go.
func validateLike(ctx context.Context, h *memoryHold, size, estimate int64, held func(int64), work func(time.Duration)) bool {
	defer h.TryReserve(0)
	if h.Reserve(ctx, mib+6*size) != nil {
		return false
	}
	held(size)
	work(1)
	if !h.TryReserve(estimate) {
		held(-size)
		if h.Reserve(ctx, estimate) != nil {
			return false
		}
		held(size)
	}
	held(estimate - size)
	work(2)
	held(-estimate)
	return true
}

// Requests of random sizes, deadlines and cancellations, of one to three
// documents each: what is reserved never exceeds the budget, nothing is
// reserved without a slot, and in the end everything is given back.
func TestMemoryBoundStress(t *testing.T) {
	const budget = 200 * mib
	s := memServer(budget, 4, 16)
	var peak peakOf
	var wg sync.WaitGroup
	var ok, busy, noSlot atomic.Int64
	requests, spread := 400, 3000
	if testing.Short() {
		requests, spread = 100, 500
	}
	for i := range requests {
		wg.Go(func() {
			r := rand.New(rand.NewSource(int64(i)))
			time.Sleep(time.Duration(r.Intn(spread)) * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(50+r.Intn(4000))*time.Millisecond)
			defer cancel()
			if r.Intn(10) == 0 {
				d := time.Duration(r.Intn(500)) * time.Millisecond
				go func() { time.Sleep(d); cancel() }()
			}
			large := r.Intn(3) > 0
			if !s.acquire(ctx, httptest.NewRecorder(), large) {
				noSlot.Add(1)
				return
			}
			h := s.newMemoryHold(large)
			defer h.release()
			for range 1 + r.Intn(3) {
				size := int64(1+r.Intn(10)) * mib
				estimate := min(size*int64(1+r.Intn(18)), budget)
				validateLike(ctx, h, size, estimate, func(int64) {}, func(time.Duration) {
					used := usedOf(s.mem)
					peak.see(used)
					if used > budget {
						t.Errorf("%d MiB reserved of %d", used/mib, budget/mib)
					}
					if h.held > 0 && !h.slot {
						t.Errorf("%d bytes reserved without a slot", h.held)
					}
					time.Sleep(time.Duration(r.Intn(15)) * time.Millisecond)
				})
			}
			if h.busy != "" {
				busy.Add(1)
			} else {
				ok.Add(1)
			}
		})
	}
	wg.Wait()
	t.Logf("%d validated, %d found no memory in time, %d no slot; at most %d MiB reserved of %d",
		ok.Load(), busy.Load(), noSlot.Load(), peak.Load()/mib, budget/mib)
	if !idle(s) || ok.Load() == 0 {
		t.Errorf("in the end: used %d, urgent %v, waiting %d, slots %d, large %d; %d validated",
			usedOf(s.mem), s.mem.urgent, s.waiting.Load(), len(s.slots), len(s.largeSlots), ok.Load())
	}
}

// Many clients send documents of 10 MiB whose rules take nearly the whole
// budget, more of them than there are slots and places to wait. Whatever
// the numbers of slots and of waiters, what is reserved stays within the
// budget, and so does what the validations really hold: the documents that
// are read and the one tree. Those that wait hold nothing. (Before, each
// waiter kept its document, and what was reserved reached 255, 418 and 494
// MiB of 192 for these three settings.)
func TestMemoryBoundHoldsWithManyWaiting(t *testing.T) {
	const budget = 192 * mib
	const size, estimate = 10 * mib, 180 * mib
	run := time.Second
	if testing.Short() {
		run = 300 * time.Millisecond
	}
	for _, cfg := range []struct{ concurrent, queue int }{{2, 8}, {8, 32}, {16, 64}} {
		s := memServer(budget, cfg.concurrent, cfg.queue)
		var reserved, held peakOf
		var holding, validated atomic.Int64
		var wg sync.WaitGroup
		deadline := time.Now().Add(run)
		for i := range cfg.concurrent + cfg.queue {
			wg.Go(func() {
				time.Sleep(time.Duration(i) * time.Millisecond)
				for time.Now().Before(deadline) {
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					if !s.acquire(ctx, httptest.NewRecorder(), true) {
						cancel()
						time.Sleep(5 * time.Millisecond)
						continue
					}
					h := s.newMemoryHold(true)
					if validateLike(ctx, h, size, estimate,
						func(d int64) { held.see(holding.Add(d)) },
						func(phase time.Duration) {
							reserved.see(usedOf(s.mem))
							time.Sleep(phase * 15 * time.Millisecond)
						}) {
						validated.Add(1)
					}
					h.release()
					cancel()
				}
			})
		}
		wg.Wait()
		t.Logf("MAX_CONCURRENT %d, MAX_QUEUE %d: %d validated; at most %d MiB reserved and %d MiB held of %d",
			cfg.concurrent, cfg.queue, validated.Load(), reserved.Load()/mib, held.Load()/mib, budget/mib)
		if reserved.Load() > budget || held.Load() > budget || validated.Load() == 0 || !idle(s) {
			t.Errorf("MAX_CONCURRENT %d, MAX_QUEUE %d: %d MiB reserved and %d MiB held of %d; %d validated; idle %v",
				cfg.concurrent, cfg.queue, reserved.Load()/mib, held.Load()/mib, budget/mib, validated.Load(), idle(s))
		}
	}
}
