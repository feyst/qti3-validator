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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"qti3-validator/internal/config"
)

// End to end with the real validator, as an attacker would: 40 clients keep
// posting a valid item of 9.7 MiB whose rules take 160 MiB, nearly all of
// the 168 MiB one document may take of the budget of 192 MiB, to a service
// with 8 slots and 32 places to wait, while a normal client posts a typical
// item five times a second.
//
// What the validations reserve stays within the budget, and so does what
// the process really holds above idle, give or take what the collector has
// not yet freed and what the HTTP server holds. (Before a validation let go
// of its document while it waited, 385 MiB was reserved and the heap
// reached 336 MiB above idle.)
//
// The typical item is served throughout, in milliseconds: it has a slot and
// a share of the memory that the large documents do not take. (Before, one
// large document that had waited two seconds kept everything else out: 1 of
// 140 items was validated, after 28.8 s, and 139 got 503.)
//
// And the large documents are validated one after the other as fast as one
// alone, each waiting for memory once, before its schema check. (Before,
// each waited a second time after its schema check, at the back of the
// queue, and many ran out of time there with that work done.)
func TestMemoryBoundUnderAttack(t *testing.T) {
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
	doc := []byte(open + strings.Repeat("<p>"+strings.Repeat("A", 1<<20)+"</p>", 8) + strings.Repeat("<p/> ", (1450<<10)/5) + `</qti-item-body></qti-assessment-item>`)
	item := testdata(t, "valid/assessment-item.xml")
	post := func(body []byte) (int, time.Duration) {
		start := time.Now()
		resp, err := http.Post(srv.URL+"/api/validate", "application/xml", bytes.NewReader(body))
		if err != nil {
			return -1, time.Since(start)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, time.Since(start)
	}
	status, alone := post(doc)
	if status != http.StatusOK {
		t.Fatalf("one document of %d MiB alone: %d", len(doc)>>20, status)
	}
	if status, _ := post(item); status != http.StatusOK {
		t.Fatalf("the item alone: %d", status)
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
	const attack = 8 * time.Second
	statuses := map[int]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	start := time.Now()
	end := start.Add(attack)
	validated := 0 // within the attack
	for range 40 {
		wg.Go(func() {
			for time.Now().Before(end) {
				status, _ := post(doc)
				mu.Lock()
				statuses[status]++
				if status == http.StatusOK && time.Now().Before(end) {
					validated++
				}
				mu.Unlock()
				if status != http.StatusOK {
					time.Sleep(100 * time.Millisecond)
				}
			}
		})
	}
	// The normal client, once the attack is under way.
	items := map[int]int{}
	var took []time.Duration
	time.Sleep(time.Second)
	for time.Now().Before(end) {
		status, d := post(item)
		items[status]++
		took = append(took, d)
		time.Sleep(200 * time.Millisecond)
	}
	wg.Wait()
	close(stop)
	<-stopped
	slices.Sort(took)
	p90 := took[len(took)*9/10]
	t.Logf("large documents: %v, %d validated in the %v of the attack; one alone takes %v", statuses, validated, attack, alone.Round(10*time.Millisecond))
	t.Logf("typical item meanwhile: %v; median %v, p90 %v, slowest %v", items, took[len(took)/2].Round(100*time.Microsecond), p90.Round(100*time.Microsecond), took[len(took)-1].Round(100*time.Microsecond))
	t.Logf("budget %d MiB: at most %d MiB reserved, and the heap at most %d MiB above idle", budget>>20, reserved.Load()>>20, heap.Load()>>20)

	for status := range statuses {
		// 422 is a validation that ran out of time, on a machine that is
		// busy with other tests.
		if status != http.StatusOK && status != http.StatusServiceUnavailable && status != http.StatusUnprocessableEntity {
			t.Errorf("large documents: %v, want only 200, 503 and 422", statuses)
		}
	}
	// One after the other they would be attack/alone; half of that allows
	// for the machine and for the schema checks that run beside each other.
	if want := int(attack / alone / 2); validated < want {
		t.Errorf("%d large documents validated in %v, want at least %d: one alone takes %v", validated, attack, want, alone)
	}
	if total := len(took); items[http.StatusOK]*100 < total*95 {
		t.Errorf("typical item during the attack: %v, want at least 95%% validated", items)
	}
	if p90 > time.Second {
		t.Errorf("typical item during the attack: p90 %v, want well under a second", p90)
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
		t.Errorf("not idle in the end: used %d, waiting %d, slots %d", usedOf(s.mem), queued(s), len(s.slots))
	}
}
