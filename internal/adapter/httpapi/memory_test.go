package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"mime/multipart"
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
	if newMemSem(0, 2) != nil {
		t.Fatal("no budget, no semaphore")
	}
	m := newMemSem(108*mib, 2) // 8 MiB for small reservations, 100 MiB for the rest
	if m.small != 8*mib || m.large != 100*mib {
		t.Fatalf("small %d MiB, large %d MiB", m.small/mib, m.large/mib)
	}
	var a, b, c reservation
	if !m.resize(&a, 60*mib) {
		t.Fatal("60 fits in 100")
	}
	if m.resize(&c, 60*mib) {
		t.Fatal("a second 60 does not fit beside the first")
	}
	if !m.resize(&b, 40*mib) || !m.resize(&b, 10*mib) || !m.resize(&b, 40*mib) {
		t.Fatal("40 fills the rest; less is always granted, and growing back fits")
	}
	if m.resize(&a, 70*mib) || a.n != 60*mib {
		t.Fatal("growing past the budget is never granted, to whoever asks")
	}
	// The one that holds 60 gives it up to wait for 70.
	m.resize(&a, 0)
	id := m.enqueue()
	if ok, _ := m.admit(id, &a, 70*mib); ok {
		t.Fatal("70 does not fit beside 40")
	}
	m.resize(&b, 0)
	if ok, _ := m.admit(id, &a, 70*mib); !ok {
		t.Fatal("room came free")
	}
	if m.usedLarge != 70*mib || m.usedSmall != 0 || len(m.urgent) != 0 || a.n != 70*mib || a.small {
		t.Fatalf("large %d, small %d, urgent %v, reservation %+v", m.usedLarge, m.usedSmall, m.urgent, a)
	}
}

// A waiter that became urgent goes before the large reservations that came
// later, and the urgent ones go in the order they first waited in:
// documents that fit cannot keep one that needs most of the budget out for
// ever, nor can those each other.
func TestMemSemUrgent(t *testing.T) {
	m := newMemSem(108*mib, 2)
	var running, a, b, c, newcomer reservation
	m.resize(&running, 50*mib)
	first, second, late := m.enqueue(), m.enqueue(), m.enqueue()
	if ok, _ := m.admit(first, &a, 90*mib); ok {
		t.Fatal("90 does not fit beside 50")
	}
	m.setUrgent(second)
	m.setUrgent(first) // later urgent, but it came first
	if m.resize(&newcomer, 10*mib) || m.resize(&running, 60*mib) {
		t.Fatal("nothing large is granted to those that run while a waiter is urgent, though it fits")
	}
	if ok, _ := m.admit(late, &c, 10*mib); ok {
		t.Fatal("a later waiter waits for the urgent ones")
	}
	if ok, _ := m.admit(second, &b, 10*mib); ok {
		t.Fatal("the second urgent waiter waits for the first, though it fits")
	}
	m.resize(&running, 0)
	if ok, _ := m.admit(first, &a, 90*mib); !ok {
		t.Fatal("the first urgent waiter gets in once there is room")
	}
	if ok, _ := m.admit(late, &c, 10*mib); ok {
		t.Fatal("the later waiter still waits for the second urgent one")
	}
	if ok, _ := m.admit(second, &b, 10*mib); !ok {
		t.Fatal("the second urgent waiter is next")
	}
	m.setUrgent(late)
	m.leave(late)
	if len(m.urgent) != 0 || m.usedLarge != 100*mib {
		t.Fatalf("urgent %v, used %d", m.urgent, m.usedLarge)
	}
}

// Small reservations have a share of their own. They are granted whenever
// they fit in it, whatever waits for the rest: a large document that waits,
// urgent or not, does not keep typical items out. And they take nothing of
// the rest: however many there are, the large one that waits gets its room.
func TestMemSemSmallShare(t *testing.T) {
	m := newMemSem(108*mib, 2)
	var running, big reservation
	m.resize(&running, 50*mib)
	id := m.enqueue()
	m.setUrgent(id)
	// Small ones come and go beside the urgent waiter.
	small := make([]reservation, 4)
	for i := range small {
		if !m.resize(&small[i], 2*mib) {
			t.Fatalf("small reservation %d: 2 MiB fits in the share of 8", i)
		}
	}
	var more reservation
	if m.resize(&more, 2*mib) {
		t.Fatal("the share is full: a small one more must wait, and takes nothing of the rest")
	}
	if ok, _ := m.admit(m.enqueue(), &more, 2*mib); ok {
		t.Fatal("nor is it admitted as a waiter")
	}
	if !m.resize(&small[0], 3*mib/2) || !m.resize(&small[1], 0) || !m.resize(&more, 2*mib) {
		t.Fatal("room in the share comes free as small ones shrink and finish")
	}
	// The large one gets all of the rest, with the share still in use.
	m.resize(&running, 0)
	if ok, _ := m.admit(id, &big, 100*mib); !ok {
		t.Fatal("the urgent waiter gets the whole large part, small ones or not")
	}
	if m.usedLarge+m.usedSmall > 108*mib || m.usedSmall != 15*mib/2 {
		t.Fatalf("large %d, small %d", m.usedLarge, m.usedSmall)
	}
	// A small reservation that grows beyond small moves to the large part,
	// when there is room; one that shrinks stays where it is.
	m.resize(&big, 80*mib)
	if !m.resize(&small[2], 20*mib) || small[2].small || m.usedLarge != 100*mib || m.usedSmall != 11*mib/2 {
		t.Fatalf("grown: %+v, large %d, small %d", small[2], m.usedLarge, m.usedSmall)
	}
	if !m.resize(&small[2], mib) || small[2].small || m.usedLarge != 81*mib {
		t.Fatalf("shrunk: %+v, large %d", small[2], m.usedLarge)
	}
	if m.resize(&small[3], 30*mib) || !small[3].small || small[3].n != 2*mib {
		t.Fatalf("no room to grow: %+v", small[3])
	}
	for _, share := range []struct {
		budget     int64
		concurrent int
		want       int64
	}{{184 * mib, 2, 8 * mib}, {440 * mib, 2, 8 * mib}, {952 * mib, 8, 32 * mib}, {32 * mib, 8, 4 * mib}, {100 * mib, 1, 4 * mib}, {100 * mib, 0, 4 * mib}} {
		if got := SmallMemoryShare(share.budget, share.concurrent); got != share.want {
			t.Errorf("share of %d MiB with %d slots: %d MiB, want %d", share.budget/mib, share.concurrent, got/mib, share.want/mib)
		}
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

const mib = 1 << 20

var largeDoc = []byte("<x>" + strings.Repeat(" ", 200) + "</x>")

func postAsync(t *testing.T, url string, body []byte, n int) chan int {
	t.Helper()
	done := make(chan int, n)
	for range n {
		go func() { done <- postStatus(t, url, "application/xml", body) }()
	}
	return done
}

// queued is the number of requests that wait, for a slot or for memory.
func queued(s *Server) int64 { return s.waiting.Load() + s.waitingSmall.Load() }

// idle reports whether the server holds nothing: no slot, no memory, nobody
// waiting.
func idle(s *Server) bool {
	if s.mem != nil {
		s.mem.mu.Lock()
		defer s.mem.mu.Unlock()
		if s.mem.usedLarge != 0 || s.mem.usedSmall != 0 || len(s.mem.urgent) != 0 {
			return false
		}
	}
	return len(s.slots) == 0 && len(s.express) == 0 && queued(s) == 0
}

// Documents that together take more memory than the budget are validated
// one after the other, however many slots there are; the one that waits
// gives up its slot, so small documents are validated beside the one that
// runs.
func TestMemoryBoundSerialisesLargeDocuments(t *testing.T) {
	v := &reserving{need: 60 * mib, release: make(chan struct{})}
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2, MaxValidationMemory: 108 * mib,
	}, v)
	url := srv.URL + "/api/validate"
	done := postAsync(t, url, largeDoc, 3)
	waitFor(t, "one running, two waiting for memory", func() bool { return v.running.Load() == 1 && queued(s) == 2 })
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
	v := &reserving{need: 30 * mib, release: make(chan struct{})}
	srv, _ := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 4, MaxValidationMemory: 108 * mib,
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
	v := &reserving{need: 60 * mib, release: make(chan struct{})}
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2, MaxQueue: 1,
		MaxValidationMemory: 108 * mib, RequestTimeout: time.Second,
	}, v)
	url := srv.URL + "/api/validate"
	first := postAsync(t, url, largeDoc, 1)
	waitFor(t, "one running", func() bool { return v.running.Load() == 1 })
	second := make(chan response, 1)
	go func() { second <- postChunked(t, url, "application/xml", largeDoc) }()
	waitFor(t, "one waiting for memory", func() bool { return queued(s) == 1 })
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
	v := &reserving{need: 105 * mib, release: make(chan struct{})}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxValidationMemory: 108 * mib}, v)
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
	for _, want := range []string{"thousand nodes", "allows 14 MiB", "MAX_VALIDATION_MEMORY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not say %q", msg, want)
		}
	}
}

// Beside the regular slots there is an express slot for small requests:
// large documents that take long on every regular slot do not keep a typical
// item waiting, and they run MaxConcurrent at a time.
func TestExpressSlotForSmallRequests(t *testing.T) {
	release := make(chan struct{})
	var running atomic.Int64
	v := fakeValidator{wait: func(context.Context) {
		running.Add(1)
		<-release
	}}
	srv, s := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2}, v)
	url := srv.URL + "/api/validate"
	large := []byte("<x>" + strings.Repeat(" ", smallRequestSize) + "</x>")
	done := postAsync(t, url, large, 3)
	waitFor(t, "two large documents running and one waiting", func() bool { return running.Load() == 2 && s.waiting.Load() == 1 })
	if len(s.slots) != 2 || len(s.express) != 0 {
		t.Fatalf("regular slots %d, express %d: the large documents take every regular slot and no other", len(s.slots), len(s.express))
	}
	small := postAsync(t, url, []byte("<x/>"), 2)
	waitFor(t, "a small document running beside them, and one waiting for it", func() bool {
		return running.Load() == 3 && s.waitingSmall.Load() == 1
	})
	if s.waiting.Load() != 1 || len(s.slots) != 2 || len(s.express) != 1 {
		t.Fatalf("waiting %d, regular slots %d, express %d", s.waiting.Load(), len(s.slots), len(s.express))
	}
	close(release)
	for _, c := range []chan int{done, done, done, small, small} {
		if status := <-c; status != http.StatusOK {
			t.Fatalf("status %d", status)
		}
	}
	waitFor(t, "everything released", func() bool { return idle(s) })

	// A small request takes the express slot first, and a regular one when
	// that is taken; a large request takes a regular one only.
	one := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1}, v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if one.tryTakeSlot(false) != expressSlot || one.tryTakeSlot(true) != regularSlot ||
		one.tryTakeSlot(true) != noSlot || one.tryTakeSlot(false) != noSlot {
		t.Fatal("with MaxConcurrent 1: one regular slot and the express slot")
	}
	one.freeSlot(regularSlot)
	if one.tryTakeSlot(false) != regularSlot {
		t.Fatal("a small request takes a regular slot when the express slot is taken")
	}
}

// packagesWait validates a document at once, and a package when released.
type packagesWait struct {
	release chan struct{}
	running atomic.Int64 // packages being validated
}

func (v *packagesWait) ValidateDocument(context.Context, app.ValidateDocument) qti.DocumentResult {
	return qti.DocumentResult{Valid: true, Schema: "qti-assessment-item", Outcome: qti.OutcomeValid}
}

func (v *packagesWait) ValidatePackage(context.Context, app.ValidatePackage) qti.PackageResult {
	v.running.Add(1)
	<-v.release
	v.running.Add(-1)
	return qti.PackageResult{Valid: true, Outcome: qti.OutcomeValid}
}

// The express slot is for single documents validated in milliseconds: a
// package never runs on it, however small its ZIP is and however it is
// sent. A ZIP under smallRequestSize may hold hundreds of documents and take
// seconds; on the express slot one client sending those kept every typical
// item waiting behind them (median 1.2 s instead of 1.5 ms, measured with a
// 181 KiB ZIP of 600 items). Packages wait for a regular slot, among the
// large requests, and typical items go on beside them.
func TestExpressSlotIsNotForPackages(t *testing.T) {
	v := &packagesWait{release: make(chan struct{})}
	srv, s := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, MaxQueue: 2}, v)
	// Also when the test fails: the server closes once its requests end.
	release := sync.OnceFunc(func() { close(v.release) })
	t.Cleanup(release)
	url := srv.URL + "/api/validate"
	pkg := zipOf(t, map[string][]byte{"imsmanifest.xml": []byte("<manifest/>")})
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile("file", "package.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(pkg)
	mw.Close()
	if len(form.Bytes()) >= smallRequestSize {
		t.Fatal("the packages of this test are to be small")
	}

	// One package runs, on the regular slot; the others wait for it.
	done := make(chan int, 3)
	for i, send := range []struct {
		how, contentType string
		body             []byte
	}{
		{"as application/zip", "application/zip", pkg},
		{"sniffed", "application/octet-stream", pkg},
		{"in a multipart upload", mw.FormDataContentType(), form.Bytes()},
	} {
		go func() { done <- postStatus(t, url, send.contentType, send.body) }()
		waitFor(t, "the package sent "+send.how+" on a regular slot or waiting for one", func() bool {
			return v.running.Load() == 1 && s.waiting.Load() == int64(i)
		})
		if len(s.slots) != 1 || len(s.express) != 0 || s.waitingSmall.Load() != 0 {
			t.Fatalf("package sent %s: regular slots %d, express %d, small requests waiting %d: a package takes a regular slot only",
				send.how, len(s.slots), len(s.express), s.waitingSmall.Load())
		}
	}

	// Typical items are validated beside them, on the express slot.
	for _, contentType := range []string{"application/xml", "application/octet-stream"} {
		if status := postStatus(t, url, contentType, []byte("<x/>")); status != http.StatusOK {
			t.Fatalf("a small document sent as %s beside the packages: status %d", contentType, status)
		}
	}
	// The packages that wait fill the queue of the large requests, not that
	// of the small ones.
	postBusy(t, url, "application/zip", pkg)
	if status := postStatus(t, url, "application/xml", []byte("<x/>")); status != http.StatusOK {
		t.Fatalf("a small document with the queue of the large requests full: status %d", status)
	}
	if v.running.Load() != 1 || s.waiting.Load() != 2 || len(s.express) != 0 {
		t.Fatalf("packages running %d, waiting %d, express %d", v.running.Load(), s.waiting.Load(), len(s.express))
	}

	release()
	for range 3 {
		if status := <-done; status != http.StatusOK {
			t.Fatalf("package: status %d", status)
		}
	}
	waitFor(t, "everything released", func() bool { return idle(s) })
}

// A small request whose validation reserves more than a small one may is a
// large one from then on: it leaves the express slot for a regular one, and
// when there is none free it waits for one, holding neither. So what runs
// beside the regular slots takes its memory from the small share only.
func TestExpressSlotIsLeftByAValidationThatGrowsLarge(t *testing.T) {
	s := memServer(108*mib, 1, 4)
	ctx := context.Background()
	slot := s.acquire(ctx, httptest.NewRecorder(), false)
	h := s.newMemoryHold(slot, false)
	if slot != expressSlot || !h.TryReserve(2*mib) || !h.held.small {
		t.Fatalf("slot %v, reservation %+v: a small request starts on the express slot, in the small share", slot, h.held)
	}
	if !h.TryReserve(20*mib) || h.slot != regularSlot || !h.large || h.held.small || len(s.express) != 0 || len(s.slots) != 1 {
		t.Fatalf("grown large: slot %v, large %v, reservation %+v, express %d, regular %d", h.slot, h.large, h.held, len(s.express), len(s.slots))
	}
	// The regular slot is taken now: the next one cannot swap, and waits.
	slot2 := s.acquire(ctx, httptest.NewRecorder(), false)
	h2 := s.newMemoryHold(slot2, false)
	if slot2 != expressSlot || !h2.TryReserve(2*mib) {
		t.Fatalf("second request: slot %v", slot2)
	}
	if h2.TryReserve(20*mib) || h2.slot != expressSlot || h2.held.n != 2*mib || len(s.slots) != 1 {
		t.Fatalf("no regular slot free: slot %v, reservation %+v, regular %d", h2.slot, h2.held, len(s.slots))
	}
	got := make(chan error, 1)
	go func() { got <- h2.Reserve(ctx, 20*mib) }()
	waitFor(t, "the second request waiting without a slot", func() bool { return s.waiting.Load() == 1 && len(s.express) == 0 && len(s.slots) == 1 })
	select {
	case err := <-got:
		t.Fatalf("it got its room without a regular slot: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	h.release()
	if err := <-got; err != nil || h2.slot != regularSlot || len(s.express) != 0 {
		t.Fatalf("after the first released: %v, slot %v, express %d", err, h2.slot, len(s.express))
	}
	h2.release()
	if !idle(s) {
		t.Fatal("not idle in the end")
	}
}

// What a request reserves from the small share it holds only while it has a
// slot. A package, or a document of 256 KiB and more, takes regular slots
// only; one that got its room in the small share and then waited for a
// regular slot with it, behind large documents that take seconds, kept
// typical items waiting for memory that long (up to 3.5 s, measured with
// four clients sending small packages beside four sending large documents).
func TestSmallShareIsNotHeldWhileWaitingForASlot(t *testing.T) {
	s := memServer(108*mib, 1, 4)
	if s.mem.small != 4*mib {
		t.Fatalf("small share %d MiB: this test needs one that holds one 3 MiB reservation", s.mem.small/mib)
	}
	ctx := context.Background()
	take := func(large bool, want slotKind) *memoryHold {
		t.Helper()
		slot := s.tryTakeSlot(large)
		if slot != want {
			t.Fatalf("got slot %v, want %v", slot, want)
		}
		return s.newMemoryHold(slot, large)
	}
	// A package has the regular slot, a typical item the express slot and
	// the small share.
	pkg, item := take(true, regularSlot), take(false, expressSlot)
	if !item.TryReserve(3 * mib) {
		t.Fatal("the item got no room")
	}
	// A document of the package finds no room, and waits without its slot,
	// which a large document takes.
	got := make(chan error, 1)
	go func() { got <- pkg.Reserve(ctx, 3*mib) }()
	waitFor(t, "the package waiting for memory without its slot", func() bool {
		return s.waiting.Load() == 1 && s.waitingSmall.Load() == 0 && len(s.slots) == 0
	})
	doc := take(true, regularSlot)
	if !doc.TryReserve(20 * mib) {
		t.Fatal("the large document got no room")
	}
	// The item finishes: the room is the package's, but there is no slot
	// for it. The next item gets the room.
	item.release()
	time.Sleep(50 * time.Millisecond) // time for the package to take the room
	next := take(false, expressSlot)
	waitFor(t, "room in the small share for the next item, while the package waits for a slot", func() bool { return next.TryReserve(3 * mib) })
	select {
	case err := <-got:
		t.Fatalf("the package went on without a slot: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	// The slot comes free while the item holds the room: the package does
	// not keep the slot to wait with.
	doc.release()
	waitFor(t, "the package waiting for memory again, without a slot", func() bool {
		return len(s.slots) == 0 && usedOf(s.mem) == 3*mib
	})
	select {
	case err := <-got:
		t.Fatalf("the package went on without room: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if s.waiting.Load() != 1 || s.waitingSmall.Load() != 0 {
		t.Fatalf("waiting %d large, %d small: the package counts as waiting all along, among the large requests", s.waiting.Load(), s.waitingSmall.Load())
	}
	next.release()
	if err := <-got; err != nil || pkg.slot != regularSlot || pkg.held.n != 3*mib || !pkg.held.small {
		t.Fatalf("the package in the end: %v, slot %v, reservation %+v", err, pkg.slot, pkg.held)
	}
	pkg.release()
	if !idle(s) {
		t.Fatal("not idle in the end")
	}

	// A large reservation is kept while its request waits for a slot, as
	// before: it waited its turn for that room.
	first, second := take(true, regularSlot), take(false, expressSlot)
	if !first.TryReserve(90*mib) || !second.TryReserve(mib) {
		t.Fatal("no room")
	}
	go func() { got <- second.Reserve(ctx, 60*mib) }()
	waitFor(t, "the second waiting for memory", func() bool { return s.waiting.Load() == 1 && len(s.express) == 0 })
	if !first.TryReserve(mib) {
		t.Fatal("the first could not shrink")
	}
	waitFor(t, "the second holding its room while it waits for the regular slot", func() bool { return usedOf(s.mem) == 61*mib })
	select {
	case err := <-got:
		t.Fatalf("the second went on without a regular slot: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	first.release()
	if err := <-got; err != nil || second.slot != regularSlot || second.held.n != 60*mib {
		t.Fatalf("the second in the end: %v, slot %v, reservation %+v", err, second.slot, second.held)
	}
	second.release()
	if !idle(s) {
		t.Fatal("not idle in the end")
	}
}

func memServer(budget int64, concurrent, queue int) *Server {
	return New(Options{MaxPackageSize: 100 * mib, MaxConcurrent: concurrent, MaxQueue: queue, MaxValidationMemory: budget, RequestTimeout: time.Minute},
		nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Entries of packages that wait for room in the small share are waiters of
// the large kind: they do not fill the queue of the typical items, which
// would get 503 for it.
func TestWaitingPackagesDoNotFillTheQueueOfItems(t *testing.T) {
	const queue = 2
	s := memServer(108*mib, 1, queue)
	if s.mem.small != 4*mib {
		t.Fatalf("small share %d MiB: this test needs one that holds one 3 MiB reservation", s.mem.small/mib)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A typical item on the express slot holds the small share.
	item := s.newMemoryHold(s.tryTakeSlot(false), false)
	if !item.TryReserve(3 * mib) {
		t.Fatal("the item got no room")
	}
	// As many packages as the queue holds each wait for room for an entry.
	done := make(chan error, queue)
	for range queue {
		pkg := s.newMemoryHold(noSlot, true)
		go func() { done <- pkg.Reserve(ctx, 3*mib) }()
	}
	waitFor(t, "the packages waiting among the large requests", func() bool {
		return s.waiting.Load() == queue && s.waitingSmall.Load() == 0
	})
	// Another item that has to wait still finds a place in its own queue.
	next := s.newMemoryHold(noSlot, false)
	got := make(chan error, 1)
	go func() { got <- next.Reserve(ctx, 3*mib) }()
	waitFor(t, "the item waiting in the queue of the items", func() bool { return s.waitingSmall.Load() == 1 })
	select {
	case err := <-got:
		t.Fatalf("the item did not wait: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	for range queue {
		<-done
	}
	<-got
	item.release()
	if queued(s) != 0 || usedOf(s.mem) != 0 {
		t.Fatalf("left: %d waiting, %d reserved", queued(s), usedOf(s.mem))
	}
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
	return m.usedLarge + m.usedSmall
}

// validateLike reserves through h as the application does for a document:
// to read it; then for its whole check, without waiting if there is room
// and else with nothing while it waits; then what the rules take. held is
// told what the validation really holds in memory: the document from when
// it is read until it is let go, and the tree while the rules run.
func validateLike(ctx context.Context, h *memoryHold, size, estimate int64, held func(int64), work func(time.Duration)) bool {
	defer h.TryReserve(0)
	read := mib + 6*size
	if h.Reserve(ctx, read) != nil {
		return false
	}
	held(size)
	if need := max(read, estimate); !h.TryReserve(need) {
		held(-size)
		if h.Reserve(ctx, need) != nil {
			return false
		}
		held(size)
	}
	work(1) // the schema check
	if !h.TryReserve(estimate) {
		panic("what the rules take is no more than what the check reserved")
	}
	held(estimate - size)
	work(2) // the rules
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
	var ok, busy, refused atomic.Int64
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
			slot := s.acquire(ctx, httptest.NewRecorder(), large)
			if slot == noSlot {
				refused.Add(1)
				return
			}
			h := s.newMemoryHold(slot, large)
			defer h.release()
			for range 1 + r.Intn(3) {
				size := int64(1+r.Intn(10)) * mib
				estimate := min(size*int64(1+r.Intn(18)), s.mem.large)
				validateLike(ctx, h, size, estimate, func(int64) {}, func(time.Duration) {
					used := usedOf(s.mem)
					peak.see(used)
					if used > budget {
						t.Errorf("%d MiB reserved of %d", used/mib, budget/mib)
					}
					if h.held.n > 0 && h.slot == noSlot {
						t.Errorf("%d bytes reserved without a slot", h.held.n)
					}
					if h.slot == expressSlot && (h.large || !h.held.small && h.held.n > 0) {
						t.Errorf("on the express slot: large %v, reservation %+v", h.large, h.held)
					}
					if n := len(s.slots); n > 4 {
						t.Errorf("%d regular slots taken of 4", n)
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
		ok.Load(), busy.Load(), refused.Load(), peak.Load()/mib, budget/mib)
	if !idle(s) || ok.Load() == 0 {
		t.Errorf("in the end: used %d, urgent %v, waiting %d, slots %d, large %d; %d validated",
			usedOf(s.mem), s.mem.urgent, queued(s), len(s.slots), len(s.express), ok.Load())
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
	const size, estimate = 10 * mib, 150 * mib
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
					slot := s.acquire(ctx, httptest.NewRecorder(), true)
					if slot == noSlot {
						cancel()
						time.Sleep(5 * time.Millisecond)
						continue
					}
					h := s.newMemoryHold(slot, true)
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
