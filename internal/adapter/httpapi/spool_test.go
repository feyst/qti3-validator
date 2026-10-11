package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/domain/qti"
)

// spooled is the budget in use.
func spooled(s *Server) int64 {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	return s.spoolUsed
}

// startRaw opens a connection and sends the start of a request to
// POST /api/validate: head (header lines, each ending in CRLF) and body.
func startRaw(t *testing.T, srv *httptest.Server, head, body string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := io.WriteString(conn, "POST /api/validate HTTP/1.1\r\nHost: test\r\n"+head+"\r\n"+body); err != nil {
		t.Fatal(err)
	}
	return conn
}

// response is what a test checks of an answer.
type response struct {
	status int
	header http.Header
	body   map[string]any
}

func readRaw(t *testing.T, conn net.Conn) response {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return decode(resp)
}

func decode(resp *http.Response) response {
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return response{status: resp.StatusCode, header: resp.Header, body: out}
}

// postChunked posts body without a Content-Length.
func postChunked(t *testing.T, url, contentType string, body []byte) response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, io.MultiReader(bytes.NewReader(body)))
	req.Header.Set("Content-Type", contentType)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return decode(resp)
}

// tempFiles counts the uploads in the temporary directory.
func tempFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// Connections that declare a large body and then stall hold exactly the
// budget for what they sent: locking other uploads out takes sending the
// whole budget, not opening many connections.
func TestSpoolStalledUploadsHoldWhatTheySent(t *testing.T) {
	const maxPackage = 100 << 20
	srv, s := newFakeServer(t, Options{MaxPackageSize: maxPackage, MaxConcurrent: 2, RequestTimeout: 60 * time.Second}, fakeValidator{})
	if s.opts.MaxSpoolSize != 4*maxPackage {
		t.Fatalf("MaxSpoolSize %d, want the default", s.opts.MaxSpoolSize)
	}
	for range 4 {
		startRaw(t, srv, fmt.Sprintf("Content-Type: application/zip\r\nContent-Length: %d\r\n", maxPackage), "PK")
	}
	waitFor(t, "four stalled uploads", func() bool { return spooled(s) == 8 })
	time.Sleep(100 * time.Millisecond)
	if got := spooled(s); got != 8 {
		t.Fatalf("four uploads of 2 bytes reserve %d bytes, want 8", got)
	}
	if status := postStatus(t, srv.URL+"/api/validate", "application/xml", []byte("<x/>")); status != http.StatusOK {
		t.Fatalf("a small document beside four stalled uploads: %d, want 200", status)
	}
}

// An upload whose next bytes do not fit in the budget gets 503 with
// Retry-After; its temporary file is removed and its reservation freed.
func TestSpoolBudgetFull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	const limit = 8 << 10
	srv, s := newFakeServer(t, Options{MaxPackageSize: limit, MaxDocumentSize: limit, MaxSpoolSize: 10 << 10, MaxConcurrent: 4},
		fakeValidator{})
	url := srv.URL + "/api/validate"

	// The first upload stalls one byte short of its 6 KiB.
	const held = 6<<10 - 1
	finish := stalledUpload(t, url, "application/zip", "PK"+strings.Repeat(" ", held-2), 6<<10)
	waitFor(t, "the first upload", func() bool { return spooled(s) == held })

	if status := postStatus(t, url, "application/zip", bytes.Repeat([]byte(" "), 3<<10)); status != http.StatusOK {
		t.Fatalf("an upload that fits beside it: %d", status)
	}
	postBusy(t, url, "application/zip", bytes.Repeat([]byte(" "), 6<<10))
	if got := postChunked(t, url, "application/zip", bytes.Repeat([]byte(" "), 6<<10)); got.status != http.StatusServiceUnavailable ||
		firstCode(got.body) != codeBusy || got.header.Get("Retry-After") == "" {
		t.Fatalf("chunked upload that does not fit: %d %v Retry-After %q", got.status, got.body, got.header.Get("Retry-After"))
	}
	waitFor(t, "the refused uploads to free their budget", func() bool { return spooled(s) == held })
	waitFor(t, "the refused uploads' files to be removed", func() bool { return tempFiles(t, dir) == 1 })

	if status := finish(); status != http.StatusOK {
		t.Fatalf("first upload: %d", status)
	}
	waitFor(t, "the release", func() bool { return spooled(s) == 0 })
	if n := tempFiles(t, dir); n != 0 {
		t.Fatalf("%d temporary files left", n)
	}
}

// Once received, a body holds the budget for its size while it is validated,
// not for the most it might have sent.
func TestSpoolShrinksToSize(t *testing.T) {
	var s *Server
	during := make(chan int64, 1)
	v := fakeValidator{wait: func(context.Context) { during <- spooled(s) }}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 100 << 20, MaxDocumentSize: 10 << 20}, v, func(srv *Server) { s = srv })
	url := srv.URL + "/api/validate"

	for _, tc := range []struct{ contentType, start string }{
		{"application/xml", "<x>"},
		{"application/zip", "PK\x03\x04"},
		{"", "<x>"},
	} {
		body := []byte(tc.start + strings.Repeat(" ", 1000-len(tc.start)))
		if got := postChunked(t, url, tc.contentType, body); got.status != http.StatusOK {
			t.Fatalf("%q: %d %v", tc.contentType, got.status, got.body)
		}
		if got := <-during; got != 1000 {
			t.Fatalf("%q: a chunked body of 1000 bytes holds %d bytes during validation", tc.contentType, got)
		}
	}
	waitFor(t, "the release", func() bool { return spooled(s) == 0 })
}

// A body larger than the whole budget can never be received: 413, without
// Retry-After.
func TestSpoolLargerThanBudget(t *testing.T) {
	const limit = 8 << 10
	srv, s := newFakeServer(t, Options{MaxPackageSize: limit, MaxDocumentSize: limit, MaxSpoolSize: 4 << 10},
		fakeValidator{})
	url := srv.URL + "/api/validate"
	wantTooLarge := func(t *testing.T, got response) {
		t.Helper()
		if got.status != http.StatusRequestEntityTooLarge || firstCode(got.body) != string(qti.CodeTooLarge) ||
			got.header.Get("Retry-After") != "" {
			t.Fatalf("got %d %v Retry-After %q, want 413 %s without Retry-After", got.status, got.body, got.header.Get("Retry-After"), qti.CodeTooLarge)
		}
		if msg, _ := got.body["message"].(string); !strings.Contains(msg, "MAX_SPOOL_SIZE") {
			t.Fatalf("message %q does not name MAX_SPOOL_SIZE", msg)
		}
		waitFor(t, "the release", func() bool { return spooled(s) == 0 })
	}

	t.Run("declared", func(t *testing.T) {
		for _, ct := range []string{"application/zip", "application/xml", "multipart/form-data; boundary=b"} {
			wantTooLarge(t, readRaw(t, startRaw(t, srv, "Content-Type: "+ct+"\r\nContent-Length: 6000\r\n", "PK")))
		}
	})
	t.Run("chunked", func(t *testing.T) {
		wantTooLarge(t, postChunked(t, url, "application/zip", bytes.Repeat([]byte(" "), 6000)))
		wantTooLarge(t, postChunked(t, url, "application/xml", bytes.Repeat([]byte(" "), 6000)))
	})
	t.Run("exactly the budget fits", func(t *testing.T) {
		body := bytes.Repeat([]byte(" "), 4<<10)
		if got := postChunked(t, url, "application/zip", body); got.status != http.StatusOK {
			t.Fatalf("chunked: %d %v", got.status, got.body)
		}
		if status := postStatus(t, url, "application/zip", body); status != http.StatusOK {
			t.Fatalf("Content-Length: %d", status)
		}
	})
}

// The budget is free again however a request ends.
func TestSpoolReleased(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	const limit = 8 << 10
	srv, s := newFakeServer(t, Options{MaxPackageSize: limit, MaxDocumentSize: limit, MaxSpoolSize: 10 << 10, RequestTimeout: 300 * time.Millisecond},
		fakeValidator{})
	url := srv.URL + "/api/validate"
	released := func(t *testing.T) {
		t.Helper()
		waitFor(t, "the release", func() bool { return spooled(s) == 0 })
		waitFor(t, "the temporary files to be removed", func() bool { return tempFiles(t, dir) == 0 })
	}

	t.Run("success", func(t *testing.T) {
		for range 10 {
			if status := postStatus(t, url, "application/zip", bytes.Repeat([]byte(" "), 6<<10)); status != http.StatusOK {
				t.Fatalf("sequential upload: %d", status)
			}
		}
		released(t)
	})
	t.Run("over the limit", func(t *testing.T) {
		if got := postChunked(t, url, "", bytes.Repeat([]byte(" "), limit+100)); got.status != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked: %d", got.status)
		}
		released(t)
		got := readRaw(t, startRaw(t, srv, "Content-Type: application/zip\r\nContent-Length: 100000\r\n", "PK"))
		if got.status != http.StatusRequestEntityTooLarge || firstCode(got.body) != string(qti.CodeTooLarge) {
			t.Fatalf("declared: %d %v", got.status, got.body)
		}
		released(t)
	})
	t.Run("read timeout", func(t *testing.T) {
		conn := startRaw(t, srv, "Content-Type: application/zip\r\nContent-Length: 6000\r\n", "PK"+strings.Repeat(" ", 2000))
		waitFor(t, "the reservation", func() bool { return spooled(s) > 0 })
		if got := readRaw(t, conn); got.status != http.StatusRequestTimeout {
			t.Fatalf("got %d %v", got.status, got.body)
		}
		released(t)
	})
	t.Run("client disconnect", func(t *testing.T) {
		conn := startRaw(t, srv, "Content-Type: application/zip\r\nContent-Length: 6000\r\n", "PK"+strings.Repeat(" ", 2000))
		waitFor(t, "the reservation", func() bool { return spooled(s) > 0 })
		_ = conn.Close()
		released(t)
	})
}
