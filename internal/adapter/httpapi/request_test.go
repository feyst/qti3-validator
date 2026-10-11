package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/config"
	"qti3-validator/internal/testutil"
)

// newTimedServer returns a server with the production http.Server settings,
// so its read deadline applies, and the given concurrency and timeout.
func newTimedServer(t *testing.T, concurrent int, timeout time.Duration) *httptest.Server {
	t.Helper()
	newTestServer(t, testOptions{}) // compiles the shared validator
	s := New(Options{
		MaxPackageSize: config.Default().MaxPackageSize, MaxConcurrent: concurrent, RequestTimeout: timeout,
	}, shared, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewUnstartedServer(nil)
	srv.Config = s.HTTPServer()
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// rawRequest sends head and body over a new connection, then waits for the
// response without sending the rest of the body.
func rawRequest(t *testing.T, srv *httptest.Server, head, body string) (int, map[string]any) {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST /api/validate HTTP/1.1\r\nHost: test\r\n"+head+"\r\n"+body); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// multipartStart is the start of a multipart body up to the file data.
const multipartStart = "--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"x.zip\"\r\n\r\nPK"

func TestBodyReadTimeout(t *testing.T) {
	srv := newTimedServer(t, 1, 500*time.Millisecond)
	for name, tc := range map[string]struct{ head, body string }{
		"xml":                     {"Content-Type: application/xml\r\nContent-Length: 1000\r\n", "<qti"},
		"zip":                     {"Content-Type: application/zip\r\nContent-Length: 1000\r\n", "PK\x03\x04"},
		"sniffed":                 {"Content-Length: 1000\r\n", "<qti"},
		"multipart data":          {"Content-Type: multipart/form-data; boundary=b\r\nContent-Length: 1000\r\n", multipartStart},
		"multipart before a part": {"Content-Type: multipart/form-data; boundary=b\r\nContent-Length: 1000\r\n", "--b\r\n"},
	} {
		t.Run(name, func(t *testing.T) {
			status, body := rawRequest(t, srv, tc.head, tc.body)
			if status != http.StatusRequestTimeout || firstCode(body) != codeRequestTimeout {
				t.Fatalf("got %d %v, want 408 %s", status, body, codeRequestTimeout)
			}
		})
	}
}

func TestMalformedBody(t *testing.T) {
	srv := newTimedServer(t, 1, 5*time.Second)
	for _, ct := range []string{"application/xml", "application/zip", ""} {
		head := "Transfer-Encoding: chunked\r\n"
		if ct != "" {
			head += "Content-Type: " + ct + "\r\n"
		}
		status, body := rawRequest(t, srv, head, "zz\r\n")
		if status != http.StatusBadRequest || firstCode(body) != codeInvalidRequest {
			t.Errorf("%q: got %d %v, want 400 %s", ct, status, body, codeInvalidRequest)
		}
	}
}

// Requests whose bodies have not arrived hold no validation slot: with one
// slot, a complete request is validated while three uploads stall.
func TestStalledUploadsHoldNoSlot(t *testing.T) {
	srv := newTimedServer(t, 1, 30*time.Second)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	_, _ = mw.CreateFormFile("file", "x.zip")
	for _, start := range []struct{ contentType, body string }{
		{"application/xml", "<qti"},
		{"application/zip", "PK\x03\x04"},
		{mw.FormDataContentType(), mp.String() + "PK"},
	} {
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.Close() })
		go func() { _, _ = io.WriteString(pw, start.body) }()
		go func() {
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/validate", pr)
			req.Header.Set("Content-Type", start.contentType)
			if resp, err := http.DefaultClient.Do(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)

	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(srv.URL+"/api/validate", "application/xml",
		bytes.NewReader(testdata(t, "valid/assessment-item.xml")))
	if err != nil {
		t.Fatalf("a complete request waited for the stalled uploads: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestPackageTooManyFiles(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	for _, ct := range []string{"application/zip", ""} {
		for _, declared := range []int{5000, 1} {
			status, body := post(t, srv.URL+"/api/validate", ct, testutil.CrowdedZIP(5000, declared, true))
			if status != http.StatusRequestEntityTooLarge || firstCode(body) != "too_many_files" {
				t.Errorf("%q declaring %d: got %d %v", ct, declared, status, body)
			}
		}
	}
}

// An empty archive starts with the end of central directory record, and is
// recognised as a ZIP without a Content-Type.
func TestEmptyZIPSniffed(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	empty := zipOf(t, nil)
	if !strings.HasPrefix(string(empty), "PK\x05\x06") {
		t.Fatalf("empty ZIP starts with %q", empty[:4])
	}
	status, body := post(t, srv.URL+"/api/validate", "", empty)
	input, _ := body["input"].(map[string]any)
	if status != http.StatusOK || input["type"] != "ZIP" || firstCode(body) == "invalid_xml" {
		t.Fatalf("got %d %v", status, body)
	}
}
