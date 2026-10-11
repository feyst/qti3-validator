package httpapi

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qti3-validator/internal/domain/qti"
)

func TestEstimateMemory(t *testing.T) {
	const mib = 1 << 20
	// A document is estimated by its own size.
	if got := estimateMemory(mib, 10*mib, false); got != validationMemoryBase+mib*validationBytesPerByte {
		t.Errorf("document estimate %d", got)
	}
	// A package is estimated by the largest entry it may hold, not its body
	// size, because entries are validated one at a time.
	small := estimateMemory(1, 2*mib, true)
	big := estimateMemory(1, 8*mib, true)
	if small >= big {
		t.Errorf("a larger MaxFileSize must raise the package estimate: %d vs %d", small, big)
	}
	if big != validationMemoryBase+8*mib*validationBytesPerByte {
		t.Errorf("package estimate %d", big)
	}
	// It never overflows.
	if got := estimateMemory(math.MaxInt64, math.MaxInt64, false); got != math.MaxInt64 {
		t.Errorf("overflow estimate %d", got)
	}
}

// The weighted semaphore admits reservations that fit, makes the rest wait,
// and always lets a reservation larger than the whole budget in alone.
func TestMemSem(t *testing.T) {
	m := newMemSem(100)
	if ok, _ := m.tryReserve(60); !ok {
		t.Fatal("60 should fit in 100")
	}
	if ok, _ := m.tryReserve(60); ok {
		t.Fatal("a second 60 must not fit beside the first")
	}
	if ok, _ := m.tryReserve(40); !ok {
		t.Fatal("40 fills the rest of 100")
	}
	m.release(60)
	m.release(40)
	// Nothing is used now, so an oversized reservation gets in alone.
	if ok, _ := m.tryReserve(1000); !ok {
		t.Fatal("an oversized reservation must be admitted when nothing is used")
	}
	if ok, _ := m.tryReserve(1); ok {
		t.Fatal("nothing more fits beside the oversized reservation")
	}
	m.release(1000)
	if newMemSem(0) != nil {
		t.Fatal("a zero budget disables the semaphore")
	}
}

// A release wakes a goroutine waiting for memory.
func TestMemSemWaitAndWake(t *testing.T) {
	m := newMemSem(100)
	m.tryReserve(100)
	admitted := make(chan struct{})
	go func() {
		for {
			ok, released := m.tryReserve(100)
			if ok {
				close(admitted)
				return
			}
			<-released
		}
	}()
	select {
	case <-admitted:
		t.Fatal("admitted while the budget was full")
	case <-time.After(50 * time.Millisecond):
	}
	m.release(100)
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("not woken after a release")
	}
}

// Two documents that each fill the memory budget never validate at the same
// time, whatever MaxConcurrent is: the second waits for the first's memory.
func TestMemoryBoundSerialisesLargeDocuments(t *testing.T) {
	var running atomic.Int64
	var peak atomic.Int64
	release := make(chan struct{})
	v := fakeValidator{wait: func(context.Context) {
		n := running.Add(1)
		for {
			if p := peak.Load(); n > p {
				if peak.CompareAndSwap(p, n) {
					break
				}
			} else {
				break
			}
		}
		<-release
		running.Add(-1)
	}}
	// Budget holds one document of ~5 KiB (estimate = base + size*ratio), and
	// MaxConcurrent is 2 so only the memory bound can serialise them.
	const size = 5 << 10
	budget := estimateMemory(size, 0, false)
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2,
		MaxValidationMemory: budget, RequestTimeout: 5 * time.Second,
	}, v)
	if s.mem == nil {
		t.Fatal("the memory bound should be enabled")
	}
	url := srv.URL + "/api/validate"
	doc := bytes.Repeat([]byte("<x/>              "), size/18)

	done := make(chan int, 2)
	for range 2 {
		go func() { done <- postStatus(t, url, "application/xml", doc) }()
	}
	waitFor(t, "one document running", func() bool { return running.Load() == 1 })
	time.Sleep(100 * time.Millisecond)
	if running.Load() != 1 {
		t.Fatalf("two large documents run at once: %d", running.Load())
	}
	close(release)
	for range 2 {
		if st := <-done; st != http.StatusOK {
			t.Fatalf("status %d", st)
		}
	}
	if peak.Load() != 1 {
		t.Fatalf("peak concurrency %d, want 1", peak.Load())
	}
}

// Many small documents run concurrently up to MaxConcurrent when the memory
// budget has room for them: the memory bound does not throttle small work.
func TestMemoryBoundAllowsSmallDocumentsConcurrently(t *testing.T) {
	var running, peak atomic.Int64
	release := make(chan struct{})
	v := fakeValidator{wait: func(context.Context) {
		n := running.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		<-release
		running.Add(-1)
	}}
	srv, _ := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 3,
		MaxValidationMemory: 1 << 30, RequestTimeout: 5 * time.Second,
	}, v)
	url := srv.URL + "/api/validate"
	doc := []byte("<x/>")
	done := make(chan int, 3)
	for range 3 {
		go func() { done <- postStatus(t, url, "application/xml", doc) }()
	}
	waitFor(t, "three documents running", func() bool { return running.Load() == 3 })
	close(release)
	for range 3 {
		<-done
	}
	if peak.Load() != 3 {
		t.Fatalf("peak concurrency %d, want 3", peak.Load())
	}
}

// A single document whose estimate exceeds the whole budget is refused with
// 413 before any validation slot is taken: it can never fit, so retrying
// cannot help.
func TestDocumentTooLargeForMemoryBudget(t *testing.T) {
	v := fakeValidator{}
	// A tiny budget: maxItem is well below the document size.
	srv, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2,
		MaxValidationMemory: validationMemoryBase + 1024, RequestTimeout: 2 * time.Second,
	}, v)
	if s.maxItem <= 0 || s.maxItem >= 1<<20 {
		t.Fatalf("maxItem %d", s.maxItem)
	}
	url := srv.URL + "/api/validate"
	doc := bytes.Repeat([]byte(" "), int(s.maxItem)+1<<10)
	status, body := post(t, url, "application/xml", doc)
	if status != http.StatusRequestEntityTooLarge || firstCode(body) != string(qti.CodeTooLarge) {
		t.Fatalf("got %d %v, want 413 too_large", status, body)
	}
	if msg, _ := body["message"].(string); !strings.Contains(msg, "MAX_VALIDATION_MEMORY") {
		t.Fatalf("message should name the setting: %v", body)
	}
}

// A package caps its per-entry size to what fits the budget, so it is
// validated rather than refused even when MaxFileSize is larger than the
// budget allows.
func TestPackageCapsEntrySizeToBudget(t *testing.T) {
	_, s := newFakeServer(t, Options{
		MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2,
		PackageLimits:       qti.PackageLimits{MaxFiles: 10, MaxFileSize: 10 << 20, MaxUncompressedSize: 20 << 20},
		MaxValidationMemory: validationMemoryBase + 1<<20, RequestTimeout: 2 * time.Second,
	}, fakeValidator{})
	est, lim, ok := s.reserveSize(nil, 500, true)
	if !ok {
		t.Fatal("a package must not be refused")
	}
	if lim.MaxFileSize != s.maxItem {
		t.Fatalf("per-entry size %d not capped to maxItem %d", lim.MaxFileSize, s.maxItem)
	}
	if est > s.opts.MaxValidationMemory {
		t.Fatalf("package estimate %d exceeds the budget %d", est, s.opts.MaxValidationMemory)
	}
}
