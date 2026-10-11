package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
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
		t.Fatal("growing past the budget beside another running validation must wait")
	}
	// The one that holds 40 parks to wait: the other is then the only one
	// running, and nobody would release if it could not go on.
	m.park(40)
	if !m.resize(60, 70) {
		t.Fatal("the only running validation is granted what it asks")
	}
	if ok, _ := m.admit(40, 50, false); ok {
		t.Fatal("a parked validation waits while another runs and there is no room")
	}
	m.resize(70, 0)
	if ok, _ := m.admit(40, 50, false); !ok {
		t.Fatal("room came free")
	}
	if m.used != 50 || m.parked != 0 {
		t.Fatalf("used %d, parked %d", m.used, m.parked)
	}
}

// When everybody waits, one goes on: validations that each hold their
// document and want more than is left cannot wait for each other.
func TestMemSemNoDeadlock(t *testing.T) {
	m := newMemSem(100)
	m.resize(0, 30)
	m.resize(0, 30)
	m.park(30)
	m.park(30)
	if ok, _ := m.admit(30, 90, false); !ok {
		t.Fatal("with nobody running, a waiter must be granted its reservation")
	}
	if ok, _ := m.admit(30, 90, false); ok {
		t.Fatal("the other waits for the one that runs now")
	}
	m.resize(90, 0)
	if ok, _ := m.admit(30, 90, false); !ok {
		t.Fatal("its turn")
	}
}

// A waiter that became urgent goes before the reservations that came later,
// so small documents cannot keep a large one out for ever.
func TestMemSemUrgent(t *testing.T) {
	m := newMemSem(100)
	m.resize(0, 50) // a running validation
	m.park(0)
	if ok, _ := m.admit(0, 90, false); ok {
		t.Fatal("90 does not fit beside 50")
	}
	m.setUrgent()
	if m.resize(0, 10) {
		t.Fatal("a newcomer waits for the urgent waiter, though it fits")
	}
	if ok, _ := m.admit(0, 10, false); ok {
		t.Fatal("a later waiter waits for the urgent one too")
	}
	m.resize(50, 0)
	if ok, _ := m.admit(0, 90, true); !ok {
		t.Fatal("the urgent waiter gets in once there is room")
	}
	if !m.resize(0, 10) {
		t.Fatal("the others go on after it")
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
	waitFor(t, "everything released", func() bool { return len(s.slots) == 0 && s.waiting.Load() == 0 })
	if s.mem.used != 0 || s.mem.parked != 0 {
		t.Fatalf("used %d, parked %d after the requests", s.mem.used, s.mem.parked)
	}
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
	waitFor(t, "everything released", func() bool { return len(s.slots) == 0 && s.mem.used == 0 && s.mem.parked == 0 })
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
