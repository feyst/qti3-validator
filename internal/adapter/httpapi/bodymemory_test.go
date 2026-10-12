package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// bodyMemoryUsed is what the bodies in memory reserve.
func bodyMemoryUsed(s *Server) int64 {
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	return s.bodyMemoryUsed
}

// noBodyMemory makes a server keep every body in a temporary file.
func noBodyMemory(s *Server) { s.bodyMemory = 0 }

// seen is what a validation was given, and where the body was at the time.
type seen struct {
	data   []byte
	again  []byte // the document read a second time, after a Seek
	files  int    // temporary files
	memory int64  // reserved in bodyMemory
	spool  int64  // reserved in MaxSpoolSize
}

// witness is a validator that reads its whole input, a document twice, and
// notes where the server kept it.
type witness struct {
	t   *testing.T
	dir string
	s   *Server
	mu  sync.Mutex
	got []seen
}

func (v *witness) note(data, again []byte) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.got = append(v.got, seen{data: data, again: again, files: tempFiles(v.t, v.dir), memory: bodyMemoryUsed(v.s), spool: spooled(v.s)})
}

// last returns what the validation of the last request saw.
func (v *witness) last(t *testing.T) seen {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.got) == 0 {
		t.Fatal("nothing was validated")
	}
	return v.got[len(v.got)-1]
}

func (v *witness) ValidateDocument(_ context.Context, q app.ValidateDocument) qti.DocumentResult {
	data, err := io.ReadAll(q.Document)
	if err != nil {
		v.t.Errorf("read the document: %v", err)
	}
	// The port's contract for a validation that waits for memory: a
	// document that can seek is read again from its start.
	seeker, ok := q.Document.(io.Seeker)
	if !ok {
		v.t.Errorf("the document is a %T, which cannot be read again", q.Document)
		return qti.DocumentResult{Outcome: qti.OutcomeInternal}
	}
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		v.t.Errorf("seek: %v", err)
	}
	again, err := io.ReadAll(q.Document)
	if err != nil {
		v.t.Errorf("read the document again: %v", err)
	}
	if q.Size != int64(len(data)) {
		v.t.Errorf("size %d of a document of %d bytes", q.Size, len(data))
	}
	v.note(data, again)
	return qti.DocumentResult{Valid: true, Schema: "qti-assessment-item", Outcome: qti.OutcomeValid}
}

func (v *witness) ValidatePackage(_ context.Context, q app.ValidatePackage) qti.PackageResult {
	data := make([]byte, q.Size)
	if n, err := q.Package.ReadAt(data, 0); n != len(data) {
		v.t.Errorf("read the package: %d of %d bytes: %v", n, len(data), err)
	}
	// As archive/zip does: from the end, in pieces.
	var tail [22]byte
	if q.Size >= int64(len(tail)) {
		if _, err := q.Package.ReadAt(tail[:], q.Size-int64(len(tail))); err != nil || !bytes.Equal(tail[:], data[len(data)-len(tail):]) {
			v.t.Errorf("read the end of the package: %v", err)
		}
	}
	v.note(data, data)
	return qti.PackageResult{Valid: true, Outcome: qti.OutcomeValid}
}

func newWitness(t *testing.T, opts Options, configure ...func(*Server)) (string, *Server, *witness) {
	t.Helper()
	v := &witness{t: t, dir: t.TempDir()}
	t.Setenv("TMPDIR", v.dir)
	srv, s := newFakeServer(t, opts, v, configure...)
	v.s = s
	return srv.URL + "/api/validate", s, v
}

// content is n bytes that differ from place to place, starting with start.
func content(start string, n int) []byte {
	b := []byte(start)
	for i := len(b); i < n; i++ {
		b = append(b, "0123456789abcdefghijklmnopqrstuvw"[(i+i/33)%33])
	}
	return b[:n]
}

// postEither posts body with a Content-Length or chunked.
func postEither(t *testing.T, url, contentType string, body []byte, chunked bool) int {
	t.Helper()
	if chunked {
		return postChunked(t, url, contentType, body).status
	}
	return postStatus(t, url, contentType, body)
}

// released waits until a server holds nothing for bodies: no budget, no
// memory, no temporary file.
func released(t *testing.T, s *Server, dir string) {
	t.Helper()
	waitFor(t, "the release of the spool", func() bool { return spooled(s) == 0 })
	waitFor(t, "the release of the memory of the bodies", func() bool { return bodyMemoryUsed(s) == 0 })
	waitFor(t, "the temporary files to be removed", func() bool { return tempFiles(t, dir) == 0 })
}

// A small body is validated from memory, whatever the shape of the request
// and with or without a Content-Length: no temporary file is made for it.
func TestSmallBodyStaysInMemory(t *testing.T) {
	url, s, v := newWitness(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20})
	document, archive := content("<x>", 2706), content("PK\x03\x04", 10647)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	part, _ := mw.CreateFormFile("file", "item.xml")
	_, _ = part.Write(document)
	_ = mw.Close()

	for _, tc := range []struct {
		name, contentType string
		body, want        []byte
	}{
		{"document", "application/xml", document, document},
		{"package", "application/zip", archive, archive},
		{"sniffed document", "", document, document},
		{"sniffed package", "application/octet-stream", archive, archive},
		{"multipart", mw.FormDataContentType(), mp.Bytes(), document},
	} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", tc.name, chunked), func(t *testing.T) {
				if status := postEither(t, url, tc.contentType, tc.body, chunked); status != http.StatusOK {
					t.Fatalf("status %d", status)
				}
				got := v.last(t)
				if !bytes.Equal(got.data, tc.want) || !bytes.Equal(got.again, tc.want) {
					t.Fatalf("validated %d bytes and, read again, %d; want the %d that were sent", len(got.data), len(got.again), len(tc.want))
				}
				if got.files != 0 || got.memory < int64(len(tc.want)) || got.memory > 2*int64(len(tc.want))+firstBodyChunk {
					t.Fatalf("%d temporary files and %d bytes of memory for a body of %d bytes", got.files, got.memory, len(tc.want))
				}
				if got.spool != int64(len(tc.want)) {
					t.Fatalf("%d bytes of MaxSpoolSize for a body of %d bytes", got.spool, len(tc.want))
				}
				released(t, s, v.dir)
			})
		}
	}
}

// The largest small body, one byte under smallRequestSize, is kept in
// memory; a body of smallRequestSize and one of a byte more go to a file.
func TestBodyMemoryThreshold(t *testing.T) {
	url, s, v := newWitness(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20})
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	part, _ := mw.CreateFormFile("file", "item.xml")
	_, _ = part.Write(content("<x>", smallRequestSize-1))
	_ = mw.Close()

	for _, tc := range []struct {
		name     string
		size     int
		inMemory bool
	}{
		{"the largest small body", smallRequestSize - 1, true},
		{"at the threshold", smallRequestSize, false},
		{"one byte over", smallRequestSize + 1, false},
	} {
		for _, chunked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/chunked=%v", tc.name, chunked), func(t *testing.T) {
				body := content("<x>", tc.size)
				if status := postEither(t, url, "application/xml", body, chunked); status != http.StatusOK {
					t.Fatalf("status %d", status)
				}
				got := v.last(t)
				if !bytes.Equal(got.data, body) || !bytes.Equal(got.again, body) {
					t.Fatalf("validated %d bytes and, read again, %d; want the %d that were sent", len(got.data), len(got.again), len(body))
				}
				if tc.inMemory && (got.files != 0 || got.memory < int64(tc.size) || got.memory > smallRequestSize) {
					t.Fatalf("%d temporary files, %d bytes of memory; want the body in memory", got.files, got.memory)
				}
				if !tc.inMemory && (got.files != 1 || got.memory != 0) {
					t.Fatalf("%d temporary files, %d bytes of memory; want the body in a file", got.files, got.memory)
				}
				released(t, s, v.dir)
			})
		}
	}
	// The file of a multipart request follows the same rule, though the
	// request around it is longer than a small body.
	t.Run("multipart", func(t *testing.T) {
		if mp.Len() < smallRequestSize {
			t.Fatal("the request should be longer than a small body")
		}
		for _, chunked := range []bool{false, true} {
			if status := postEither(t, url, mw.FormDataContentType(), mp.Bytes(), chunked); status != http.StatusOK {
				t.Fatalf("status %d", status)
			}
			if got := v.last(t); got.files != 0 || len(got.data) != smallRequestSize-1 {
				t.Fatalf("chunked=%v: %d temporary files for a file of %d bytes", chunked, got.files, len(got.data))
			}
			released(t, s, v.dir)
		}
	})
}

// When the memory for bodies is taken, a small body goes to a temporary
// file like a large one: the request is validated, not refused.
func TestBodyMemoryFullFallsBackToFile(t *testing.T) {
	url, s, v := newWitness(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2},
		func(s *Server) { s.bodyMemory = 8 << 10 })

	// A stalled upload of 7 KiB holds 7 KiB of the 8, and a byte.
	const held = 7<<10 + 1
	finish := stalledUpload(t, url, "application/xml", "<x>"+strings.Repeat(" ", 6000), 7<<10)
	waitFor(t, "the stalled upload", func() bool { return spooled(s) == 6003 })
	if got := bodyMemoryUsed(s); got != held {
		t.Fatalf("a stalled upload of 7 KiB holds %d bytes of memory", got)
	}
	for _, chunked := range []bool{false, true} {
		body := content("<x>", 3000)
		if status := postEither(t, url, "application/xml", body, chunked); status != http.StatusOK {
			t.Fatalf("chunked=%v: a small body beside it: %d, want 200", chunked, status)
		}
		got := v.last(t)
		if !bytes.Equal(got.data, body) || got.files != 1 || got.memory != held {
			t.Fatalf("chunked=%v: %d bytes validated, %d temporary files, %d bytes of memory; want the body in a file", chunked, len(got.data), got.files, got.memory)
		}
	}
	// A body that fits in what is left stays in memory.
	if status := postStatus(t, url, "application/xml", content("<x>", 1000)); status != http.StatusOK {
		t.Fatalf("a body of 1000 bytes: %d", status)
	}
	if got := v.last(t); got.files != 0 || got.memory != held+1001 {
		t.Fatalf("a body of 1000 bytes: %d temporary files, %d bytes of memory", got.files, got.memory)
	}
	if status := finish(); status != http.StatusOK {
		t.Fatalf("the stalled upload, completed: %d", status)
	}
	if got := v.last(t); got.files != 0 || len(got.data) != 7<<10 {
		t.Fatalf("the stalled upload: %d temporary files, %d bytes", got.files, len(got.data))
	}
	released(t, s, v.dir)
}

// A body that starts in memory and then finds no more room there, or grows
// past a small one, moves to a temporary file with every byte it sent, and
// gives its memory back.
func TestBodyMovesToFileMidBody(t *testing.T) {
	t.Run("no room for the next piece", func(t *testing.T) {
		// Room for the first piece of a chunked body, not for the second.
		url, s, v := newWitness(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20},
			func(s *Server) { s.bodyMemory = firstBodyChunk + 100 })
		for _, contentType := range []string{"application/xml", "application/zip"} {
			body := content("PK\x03\x04", 20000)
			if got := postChunked(t, url, contentType, body); got.status != http.StatusOK {
				t.Fatalf("%s: %d %v", contentType, got.status, got.body)
			}
			got := v.last(t)
			if !bytes.Equal(got.data, body) || !bytes.Equal(got.again, body) {
				t.Fatalf("%s: the %d bytes validated are not the %d sent", contentType, len(got.data), len(body))
			}
			if got.files != 1 || got.memory != 0 || got.spool != int64(len(body)) {
				t.Fatalf("%s: %d temporary files, %d bytes of memory, %d of the spool", contentType, got.files, got.memory, got.spool)
			}
			released(t, s, v.dir)
		}
	})
	t.Run("past a small body", func(t *testing.T) {
		url, s, v := newWitness(t, Options{MaxPackageSize: 1 << 20, MaxDocumentSize: 1 << 20})
		// The body stalls in memory, and goes on to be a large one.
		start, rest := string(content("<x>", 100<<10)), strings.Repeat("y", 300<<10)
		pr, pw := io.Pipe()
		t.Cleanup(func() { _ = pw.CloseWithError(io.ErrUnexpectedEOF) })
		more, done := make(chan struct{}), make(chan int, 1)
		go func() {
			_, _ = io.WriteString(pw, start)
			<-more
			_, _ = io.WriteString(pw, rest)
			_ = pw.Close()
		}()
		go func() {
			req, _ := http.NewRequest(http.MethodPost, url, pr)
			req.Header.Set("Content-Type", "application/xml")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				done <- 0
				return
			}
			resp.Body.Close()
			done <- resp.StatusCode
		}()
		waitFor(t, "the start of the body in memory", func() bool { return spooled(s) == int64(len(start)) })
		if files, memory := tempFiles(t, v.dir), bodyMemoryUsed(s); files != 0 || memory < int64(len(start)) {
			t.Fatalf("%d temporary files, %d bytes of memory for the first %d bytes", files, memory, len(start))
		}
		close(more)
		if status := <-done; status != http.StatusOK {
			t.Fatalf("the completed upload: %d", status)
		}
		got := v.last(t)
		if want := start + rest; string(got.data) != want || string(got.again) != want {
			t.Fatalf("the %d bytes validated are not the %d sent", len(got.data), len(want))
		}
		if got.files != 1 || got.memory != 0 {
			t.Fatalf("%d temporary files, %d bytes of memory; want the body in a file", got.files, got.memory)
		}
		released(t, s, v.dir)
	})
}

// Many connections that each send part of a small body and then stall hold
// no more memory than bodyMemory: the bodies that find no room there are in
// temporary files, by the byte, and a complete request is still validated.
func TestBodyMemoryBoundUnderStalledUploads(t *testing.T) {
	const budget, connections, sent = 1 << 20, 60, 100 << 10
	url, s, v := newWitness(t, Options{MaxPackageSize: 4 << 20, MaxDocumentSize: 1 << 20, MaxConcurrent: 2, RequestTimeout: 60 * time.Second},
		func(s *Server) { s.bodyMemory = budget })
	address := strings.TrimSuffix(strings.TrimPrefix(url, "http://"), "/api/validate")
	head := fmt.Sprintf("POST /api/validate HTTP/1.1\r\nHost: test\r\nContent-Type: application/xml\r\nContent-Length: %d\r\n\r\n", smallRequestSize-1)
	start := head + string(content("<x>", sent))
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()
	most := int64(0)
	for range connections {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		if _, err := io.WriteString(conn, start); err != nil {
			t.Fatal(err)
		}
		most = max(most, bodyMemoryUsed(s))
	}
	waitFor(t, "the stalled uploads", func() bool {
		most = max(most, bodyMemoryUsed(s))
		return spooled(s) == connections*sent
	})
	inFiles := tempFiles(t, v.dir)
	t.Logf("%d stalled uploads of %d KiB each: at most %d KiB of memory of %d, %d of them in temporary files",
		connections, sent>>10, most>>10, budget>>10, inFiles)
	if most > budget {
		t.Fatalf("the bodies in memory reserved %d bytes of %d", most, budget)
	}
	// What is in memory is at most twice what was sent, so at least a
	// quarter of the budget is in use, and the rest of the uploads are in
	// files.
	if most < budget/4 || inFiles == 0 || inFiles >= connections {
		t.Fatalf("%d bytes of memory in use and %d of %d uploads in files", most, inFiles, connections)
	}
	body := content("<x>", 200<<10)
	if status := postStatus(t, url, "application/xml", body); status != http.StatusOK {
		t.Fatalf("a complete request beside the stalled uploads: %d", status)
	}
	if got := v.last(t); !bytes.Equal(got.data, body) || got.memory > budget {
		t.Fatalf("validated %d bytes with %d bytes of memory reserved", len(got.data), got.memory)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	released(t, s, v.dir)
}

// A body in memory is read at any offset like the bytes it holds, across
// the pieces it arrived in.
func TestUploadReadAt(t *testing.T) {
	want := content("PK", 40000)
	for _, hint := range []int64{-1, 40000, 50000, 600} {
		s := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1}, fakeValidator{}, nil)
		res := s.newSpoolReservation()
		u := &upload{}
		// One byte at a time, then all there is: pieces of every size.
		if err := u.readMemory(io.MultiReader(iotestOneByte(want[:700]), bytes.NewReader(want[700:])), hint, res); err != nil {
			t.Fatalf("hint %d: %v", hint, err)
		}
		if u.size != int64(len(want)) || len(u.chunks) < 2 || res.n != u.size {
			t.Fatalf("hint %d: size %d in %d pieces, %d reserved", hint, u.size, len(u.chunks), res.n)
		}
		held := int64(0)
		for _, c := range u.chunks {
			held += int64(cap(c))
		}
		if held != res.mem || held != s.bodyMemoryUsed || held > 2*u.size+firstBodyChunk {
			t.Fatalf("hint %d: the pieces hold %d bytes, %d reserved, %d in use", hint, held, res.mem, s.bodyMemoryUsed)
		}
		ref := bytes.NewReader(want)
		for _, at := range []struct{ off, n int }{{0, 40000}, {0, 1}, {600, 2}, {4095, 3}, {16383, 10}, {700, 30000}, {39990, 10}, {39990, 20}, {40000, 1}, {50000, 4}, {3, 0}} {
			got, exp := make([]byte, at.n), make([]byte, at.n)
			n, err := u.ReadAt(got, int64(at.off))
			m, expErr := ref.ReadAt(exp, int64(at.off))
			if n != m || (err == nil) != (expErr == nil) || !bytes.Equal(got[:n], exp[:m]) {
				t.Fatalf("hint %d: ReadAt(%d bytes at %d): %d, %v; want %d, %v", hint, at.n, at.off, n, err, m, expErr)
			}
		}
		if _, err := u.ReadAt(make([]byte, 1), -1); err == nil {
			t.Fatal("a negative offset is an error")
		}
		all, err := io.ReadAll(u.reader())
		if err != nil || !bytes.Equal(all, want) {
			t.Fatalf("hint %d: read %d bytes: %v", hint, len(all), err)
		}
		res.release()
		if s.bodyMemoryUsed != 0 || s.spoolUsed != 0 {
			t.Fatalf("hint %d: %d bytes of memory and %d of the spool left", hint, s.bodyMemoryUsed, s.spoolUsed)
		}
	}
}

// iotestOneByte reads b one byte at a time.
func iotestOneByte(b []byte) io.Reader { return &oneByte{b: b} }

type oneByte struct{ b []byte }

func (r *oneByte) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0], r.b = r.b[0], r.b[1:]
	return 1, nil
}

// failOnce is an app.Memory that has no room the first time a validation
// asks for some without waiting, so the validation waits, and then reads
// its document again.
type failOnce struct {
	held    int64
	refused int
	waited  int
}

func (m *failOnce) TryReserve(n int64) bool {
	if n > 0 && m.refused == 0 {
		m.refused++
		return false
	}
	m.held = n
	return true
}

func (m *failOnce) Reserve(_ context.Context, n int64) error {
	m.waited++
	m.held = n
	return nil
}

func (*failOnce) Limit() int64 { return 1 << 40 }

// The application reads a document again from a body in memory after it
// waited for memory, as it does from a file: the verdict is the same.
func TestValidationRereadsBodyInMemory(t *testing.T) {
	newTestServer(t, testOptions{}) // compiles the shared validator
	for _, name := range []string{"valid/assessment-item.xml", "invalid/invalid-value.xml"} {
		doc := testdata(t, name)
		want := shared.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(doc), Size: int64(len(doc))})
		for _, pieces := range []bool{false, true} {
			s := New(Options{MaxPackageSize: 1 << 20, MaxConcurrent: 1}, shared, nil)
			res := s.newSpoolReservation()
			u, in, hint := &upload{}, io.Reader(bytes.NewReader(doc)), int64(len(doc))
			if pieces {
				in, hint = iotestOneByte(doc), 600 // less than it is, so it comes in pieces
			}
			if err := u.readMemory(in, hint, res); err != nil {
				t.Fatal(err)
			}
			if pieces != (len(u.chunks) > 1) {
				t.Fatalf("%s: %d bytes in %d pieces", name, len(doc), len(u.chunks))
			}
			m := &failOnce{}
			got := shared.ValidateDocument(app.WithMemory(context.Background(), m), app.ValidateDocument{Document: u.reader(), Size: u.size})
			// Once to read the document, and once more after there was no
			// room for its check.
			if m.refused != 1 || m.waited != 2 {
				t.Fatalf("%s: refused %d times, waited %d times; want the validation to wait for its check", name, m.refused, m.waited)
			}
			if got.Outcome != want.Outcome || got.Valid != want.Valid || len(got.Errors) != len(want.Errors) || got.Schema != want.Schema {
				t.Fatalf("%s, in pieces %v: %+v after waiting, want %+v", name, pieces, got, want)
			}
			res.release()
		}
	}
}
