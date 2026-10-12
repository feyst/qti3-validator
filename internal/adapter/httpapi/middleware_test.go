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

// logBuffer collects log lines written from the server's goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// lines returns the JSON log lines written so far.
func (b *logBuffer) lines(t *testing.T) []map[string]any {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var lines []map[string]any
	for line := range strings.Lines(b.buf.String()) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		lines = append(lines, entry)
	}
	return lines
}

// panicking is a validator that panics with what.
type panicking struct{ what any }

func (v panicking) ValidateDocument(context.Context, app.ValidateDocument) qti.DocumentResult {
	panic(v.what)
}

func (v panicking) ValidatePackage(context.Context, app.ValidatePackage) qti.PackageResult {
	panic(v.what)
}

// A panic in a handler is one line in the log, at error level, with the
// request, what the panic said and where it came from. The client gets a
// 500 that says none of that, and the request gives back what it held.
func TestPanicIsLoggedAndNotShown(t *testing.T) {
	const secret = "index out of range in /srv/internal/secret.go"
	var out logBuffer
	s := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, MaxValidationMemory: 64 << 20, RequestTimeout: 10 * time.Second},
		panicking{what: secret}, slog.New(slog.NewJSONHandler(&out, nil)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/validate?name=item.xml", "application/xml", strings.NewReader("<x/>"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "internal error") {
		t.Fatalf("got %d %s, want 500 internal error", resp.StatusCode, body)
	}
	for _, internal := range []string{"secret", "index out of range", "goroutine", ".go"} {
		if strings.Contains(string(body), internal) {
			t.Errorf("the response shows %q: %s", internal, body)
		}
	}

	var lines []map[string]any
	waitFor(t, "the request in the log", func() bool {
		lines = out.lines(t)
		return len(lines) == 2
	})
	failure, request := lines[0], lines[1]
	if failure["level"] != "ERROR" || failure["msg"] != "panic" || failure["method"] != "POST" || failure["path"] != "/api/validate" ||
		failure["panic"] != secret {
		t.Errorf("the panic in the log: %v", failure)
	}
	if stack, _ := failure["stack"].(string); !strings.Contains(stack, "panicking.ValidateDocument") || !strings.Contains(stack, "apiValidate") {
		t.Errorf("the stack in the log does not show where the panic came from: %q", stack)
	}
	if request["msg"] != "request" || request["status"] != float64(http.StatusInternalServerError) || request["level"] != "INFO" {
		t.Errorf("the request in the log: %v", request)
	}
	waitFor(t, "the slot, the memory and the body given back", func() bool {
		return idle(s) && spooled(s) == 0 && bodyMemoryUsed(s) == 0
	})
}

// http.ErrAbortHandler aborts the response and is no failure: the
// connection is closed, and the log has the request and no error.
func TestAbortHandlerIsPassedOn(t *testing.T) {
	var out logBuffer
	s := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, RequestTimeout: 10 * time.Second},
		panicking{what: http.ErrAbortHandler}, slog.New(slog.NewJSONHandler(&out, nil)))
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = s.HTTPServer()
	srv.Start()
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/validate", "application/xml", strings.NewReader("<x/>"))
	if err == nil {
		resp.Body.Close()
		t.Fatalf("got status %d, want the connection closed without a response", resp.StatusCode)
	}
	var lines []map[string]any
	waitFor(t, "the request in the log", func() bool {
		lines = out.lines(t)
		return len(lines) == 1
	})
	if lines[0]["msg"] != "request" || lines[0]["level"] != "INFO" {
		t.Errorf("log: %v", lines)
	}
	waitFor(t, "the slot given back", func() bool { return idle(s) })
}

// What net/http reports by itself is a line in the log of the service.
func TestServerErrorsGoToTheLog(t *testing.T) {
	var out logBuffer
	s := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1, RequestTimeout: 10 * time.Second},
		fakeValidator{}, slog.New(slog.NewJSONHandler(&out, nil)))
	s.HTTPServer().ErrorLog.Print("http: Accept error: too many open files; retrying in 5ms")
	lines := out.lines(t)
	if len(lines) != 1 || lines[0]["level"] != "WARN" || lines[0]["source"] != "net/http" ||
		lines[0]["msg"] != "http: Accept error: too many open files; retrying in 5ms" {
		t.Fatalf("log: %v", lines)
	}
}
