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

// The requests waiting for a slot are bounded: beyond MaxQueue, 503 at once.
func TestQueueCap(t *testing.T) {
	release := make(chan struct{})
	v := fakeValidator{wait: func(context.Context) { <-release }}
	srv, s := newFakeServer(t, Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, MaxQueue: 1}, v)
	url := srv.URL + "/api/validate"
	doc := []byte("<x/>")

	// Two small requests run, on the regular slot and on the express slot.
	results := make(chan int, 3)
	for range 3 {
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
	waitFor(t, "two running and one waiting", func() bool {
		return len(s.slots) == 1 && len(s.express) == 1 && s.waitingSmall.Load() == 1
	})

	postBusy(t, url, "application/xml", doc)

	close(release)
	for range 3 {
		if status := <-results; status != http.StatusOK {
			t.Fatalf("running or waiting request: %d", status)
		}
	}
	if s.waitingSmall.Load() != 0 {
		t.Fatalf("waiting: %d", s.waitingSmall.Load())
	}
	// The refused request and the others gave back what their bodies held.
	waitFor(t, "the release", func() bool { return spooled(s) == 0 && bodyMemoryUsed(s) == 0 })
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
