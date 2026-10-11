package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync"
	"testing"
	"time"

	"qti3-validator/internal/config"
)

// End to end with the real validator, as an attacker would: 40 clients keep
// posting a valid item of 9.8 MiB whose rules take 185 MiB, nearly the whole
// budget of 192 MiB that a container of 256 MiB gets, to a service with 8
// slots and 32 places to wait. What the validations reserve stays within
// the budget, and so does what the process really holds above idle, give or
// take what the collector has not yet freed and what the HTTP server holds.
// (Before, when a validation kept its document while it waited, 385 MiB was
// reserved and the heap reached 336 MiB above idle.)
func TestMemoryBoundHoldsTheHeap(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("measures the heap: not with the race detector, which changes it, nor in short mode")
	}
	newTestServer(t, testOptions{}) // compiles the shared validator
	const budget = 192 << 20
	cfg := config.Default()
	s := New(Options{
		MaxPackageSize: cfg.MaxPackageSize, MaxConcurrent: 8, MaxQueue: 32,
		MaxValidationMemory: budget, RequestTimeout: 20 * time.Second,
	}, shared, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	const open = `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false"><qti-item-body>`
	doc := []byte(open + strings.Repeat("<p>"+strings.Repeat("A", 1<<20)+"</p>", 8) + strings.Repeat("<p/> ", (1750<<10)/5) + `</qti-item-body></qti-assessment-item>`)
	post := func() int {
		resp, err := http.Post(srv.URL+"/api/validate", "application/xml", bytes.NewReader(doc))
		if err != nil {
			return -1
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if status := post(); status != http.StatusOK {
		t.Fatalf("one document of %d MiB alone: %d", len(doc)>>20, status)
	}

	old := debug.SetGCPercent(10)
	defer debug.SetGCPercent(old)
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	read := func() int64 { metrics.Read(sample); return int64(sample[0].Value.Uint64()) }
	base := read()
	var heap, reserved peakOf
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			heap.see(read() - base)
			reserved.see(usedOf(s.mem))
			time.Sleep(time.Millisecond)
		}
	}()
	statuses := map[int]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	end := time.Now().Add(6 * time.Second)
	for range 40 {
		wg.Go(func() {
			for time.Now().Before(end) {
				status := post()
				mu.Lock()
				statuses[status]++
				mu.Unlock()
				if status != http.StatusOK {
					time.Sleep(100 * time.Millisecond)
				}
			}
		})
	}
	wg.Wait()
	close(stop)
	<-stopped
	t.Logf("statuses %v; budget %d MiB: at most %d MiB reserved, and the heap at most %d MiB above idle",
		statuses, budget>>20, reserved.Load()>>20, heap.Load()>>20)
	if statuses[http.StatusOK] < 2 {
		t.Errorf("statuses %v: want documents validated", statuses)
	}
	for status := range statuses {
		// 422 is a validation that ran out of time, on a machine that is
		// busy with other tests.
		if status != http.StatusOK && status != http.StatusServiceUnavailable && status != http.StatusUnprocessableEntity {
			t.Errorf("statuses %v: want only 200, 503 and 422", statuses)
		}
	}
	if reserved.Load() > budget {
		t.Errorf("%d MiB reserved of %d", reserved.Load()>>20, budget>>20)
	}
	// A tenth for the collector, which runs when the heap has grown by that
	// much, and 32 MiB for the connections and the reports.
	if limit := int64(budget + budget/10 + 32<<20); heap.Load() > limit {
		t.Errorf("the heap reached %d MiB above idle, with a budget of %d MiB", heap.Load()>>20, budget>>20)
	}
	if !idle(s) {
		t.Errorf("not idle in the end: used %d, waiting %d, slots %d", usedOf(s.mem), s.waiting.Load(), len(s.slots))
	}
}
