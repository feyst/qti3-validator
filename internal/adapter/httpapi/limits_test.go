package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// fakeValidator answers every document and package with a valid result,
// after wait returns.
type fakeValidator struct {
	wait func(ctx context.Context)
}

func (f fakeValidator) ValidateDocument(ctx context.Context, _ app.ValidateDocument) qti.DocumentResult {
	if f.wait != nil {
		f.wait(ctx)
	}
	return qti.DocumentResult{Valid: true, Schema: "qti-assessment-item", Outcome: qti.OutcomeValid}
}

func (f fakeValidator) ValidatePackage(ctx context.Context, _ app.ValidatePackage) qti.PackageResult {
	if f.wait != nil {
		f.wait(ctx)
	}
	return qti.PackageResult{Valid: true, Outcome: qti.OutcomeValid}
}

// newFakeServer returns a server with the production http.Server settings
// around v, and the Server itself for its counters.
func newFakeServer(t *testing.T, opts Options, v Validator, configure ...func(*Server)) (*httptest.Server, *Server) {
	t.Helper()
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = 10 * time.Second
	}
	if opts.MaxConcurrent == 0 {
		opts.MaxConcurrent = 1
	}
	s := New(opts, v, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, c := range configure {
		c(s)
	}
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = s.HTTPServer()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, s
}

// stalledUpload starts a request whose body sends start and then waits until
// the returned function is called, which completes the body and waits for
// the response. With length >= 0 the request declares that Content-Length,
// else it is chunked.
func stalledUpload(t *testing.T, url, contentType, start string, length int64) (finish func() int) {
	t.Helper()
	pr, pw := io.Pipe()
	req, _ := http.NewRequest(http.MethodPost, url, pr)
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = length
	if length == 0 {
		t.Fatal("length 0 sends no body")
	}
	done := make(chan int, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			done <- 0
			return
		}
		resp.Body.Close()
		done <- resp.StatusCode
	}()
	go func() { _, _ = io.WriteString(pw, start) }()
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { _ = pw.CloseWithError(io.ErrUnexpectedEOF) }) })
	return func() int {
		once.Do(func() {
			rest := ""
			if length > 0 {
				rest = strings.Repeat(" ", int(length)-len(start))
			}
			_, _ = io.WriteString(pw, rest)
			_ = pw.Close()
		})
		return <-done
	}
}

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 500 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// postBusy posts body and checks for an immediate 503 busy with Retry-After.
func postBusy(t *testing.T, url, contentType string, body []byte) {
	t.Helper()
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("want an immediate 503: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusServiceUnavailable || firstCode(out) != codeBusy || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("got %d %v Retry-After %q, want 503 busy with Retry-After", resp.StatusCode, out, resp.Header.Get("Retry-After"))
	}
}

func postStatus(t *testing.T, url, contentType string, body []byte) int {
	t.Helper()
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// spooled is the budget in use.
func spooled(s *Server) int64 {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	return s.spoolUsed
}

// The bytes all requests may be spooling at once are bounded: a request that
// does not fit gets 503 at once, and the budget is free again when requests
// finish.
func TestSpoolBudget(t *testing.T) {
	const limit = 8 << 10
	srv, s := newFakeServer(t, Options{MaxPackageSize: limit, MaxDocumentSize: limit, MaxSpoolSize: 10 << 10, MaxConcurrent: 4}, fakeValidator{})
	url := srv.URL + "/api/validate"
	small := bytes.Repeat([]byte(" "), 3<<10)

	t.Run("chunked reserves the limit", func(t *testing.T) {
		finish := stalledUpload(t, url, "application/zip", "PK", -1)
		waitFor(t, "the reservation", func() bool { return spooled(s) == limit+1 })
		postBusy(t, url, "application/zip", small)
		postBusy(t, url, "application/xml", small)
		if status := finish(); status != http.StatusOK {
			t.Fatalf("stalled upload: %d", status)
		}
		waitFor(t, "the release", func() bool { return spooled(s) == 0 })
	})

	t.Run("Content-Length reserves the length", func(t *testing.T) {
		finish := stalledUpload(t, url, "application/zip", "PK", 4<<10)
		waitFor(t, "the reservation", func() bool { return spooled(s) == 4<<10 })
		if status := postStatus(t, url, "application/zip", small); status != http.StatusOK {
			t.Fatalf("an upload that fits beside it: %d", status)
		}
		postBusy(t, url, "application/zip", bytes.Repeat([]byte(" "), 7<<10))
		if status := finish(); status != http.StatusOK {
			t.Fatalf("stalled upload: %d", status)
		}
	})

	t.Run("released after use", func(t *testing.T) {
		for range 10 {
			if status := postStatus(t, url, "application/zip", bytes.Repeat([]byte(" "), 6<<10)); status != http.StatusOK {
				t.Fatalf("sequential upload: %d", status)
			}
		}
		if status := postStatus(t, url, "", bytes.Repeat([]byte(" "), limit+100)); status != http.StatusRequestEntityTooLarge {
			t.Fatalf("too large: %d", status)
		}
		waitFor(t, "the release", func() bool { return spooled(s) == 0 })
	})

	t.Run("a declared length above the limit is refused", func(t *testing.T) {
		status, body := rawRequest(t, srv, "Content-Type: application/zip\r\nContent-Length: 100000\r\n", "PK")
		if status != http.StatusRequestEntityTooLarge || firstCode(body) != string(qti.CodeTooLarge) {
			t.Fatalf("got %d %v", status, body)
		}
		if spooled(s) != 0 {
			t.Fatalf("budget in use: %d", spooled(s))
		}
	})
}

// The requests waiting for a slot are bounded: beyond MaxQueue, 503 at once.
func TestQueueCap(t *testing.T) {
	release := make(chan struct{})
	v := fakeValidator{wait: func(context.Context) { <-release }}
	srv, s := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, MaxQueue: 1}, v)
	url := srv.URL + "/api/validate"
	doc := []byte("<x/>")

	results := make(chan int, 2)
	for range 2 {
		go func() {
			client := http.Client{Timeout: 5 * time.Second}
			resp, err := client.Post(url, "application/xml", bytes.NewReader(doc))
			if err != nil {
				results <- 0
				return
			}
			resp.Body.Close()
			results <- resp.StatusCode
		}()
	}
	waitFor(t, "one running and one waiting", func() bool { return len(s.slots) == 1 && s.waiting.Load() == 1 })

	postBusy(t, url, "application/xml", doc)

	close(release)
	for range 2 {
		if status := <-results; status != http.StatusOK {
			t.Fatalf("running or waiting request: %d", status)
		}
	}
	if s.waiting.Load() != 0 {
		t.Fatalf("waiting: %d", s.waiting.Load())
	}
}

// Work that outlasts the read deadline still gets its response written: the
// write deadline is extended once validation returns.
func TestResponseAfterDeadline(t *testing.T) {
	const timeout = 200 * time.Millisecond
	const grace = 300 * time.Millisecond
	// The validator returns after the request timeout and after the server's
	// own write timeout would have passed.
	v := fakeValidator{wait: func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(timeout + grace)
	}}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20, RequestTimeout: timeout}, v,
		func(s *Server) { s.writeGrace = grace })
	status, body := rawRequest(t, srv, "Content-Type: application/xml\r\nContent-Length: 4\r\n", "<x/>")
	if status != http.StatusOK || outcome(body) != "VALID" {
		t.Fatalf("got %d %v", status, body)
	}
}
