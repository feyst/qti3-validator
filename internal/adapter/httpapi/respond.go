package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"runtime"

	"qti3-validator/internal/domain/qti"
)

// Codes of request errors: the request could not be validated at all. Codes
// of findings are in package qti.
const (
	codeInvalidRequest   = "invalid_request"
	codeUnknownValidator = "unknown_validator"
	codeBusy             = "busy"
	codeRequestTimeout   = "request_timeout"
)

// requestError is the body of a response without a report.
type requestError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, requestError{Code: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// statusFor maps a validation outcome to the HTTP status of its report. A
// report is HTTP 200 whatever it found, as with 1EdTech's validators: the
// verdict is summary.outcome. A validation that stopped before the end,
// because the input needs too much work or time, is 422 with the findings so
// far; a failure of the service itself is 500.
func statusFor(o qti.Outcome) int {
	switch o {
	case qti.OutcomeInternal:
		return http.StatusInternalServerError
	case qti.OutcomeTooComplex:
		return http.StatusUnprocessableEntity
	}
	return http.StatusOK
}

// mediaType is the request's media type, without parameters; empty when the
// header is missing or malformed.
func mediaType(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mt
}

// upload is a request body received in full: in memory when it is small
// and there was room for it (see Server.bodyMemory), else in a temporary
// file. The caller closes it.
type upload struct {
	chunks [][]byte // the body in memory, in the order it arrived
	file   *os.File // the body in a temporary file; nil when in memory
	size   int64
}

var _ io.ReaderAt = (*upload)(nil)

// ReadAt implements io.ReaderAt, which reading a ZIP takes.
func (u *upload) ReadAt(p []byte, off int64) (int, error) {
	if u.file != nil {
		return u.file.ReadAt(p, off)
	}
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	n := 0
	for _, c := range u.chunks {
		if off >= int64(len(c)) {
			off -= int64(len(c))
			continue
		}
		n += copy(p[n:], c[off:])
		off = 0
		if n == len(p) {
			return n, nil
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// reader reads the body from its start, and again after a Seek: a
// validation that waited for memory reads its document a second time.
func (u *upload) reader() io.ReadSeeker {
	if u.file == nil && len(u.chunks) == 1 {
		return bytes.NewReader(u.chunks[0])
	}
	return io.NewSectionReader(u, 0, u.size)
}

// close removes the temporary file, if there is one.
func (u *upload) close() {
	if u.file != nil {
		_ = u.file.Close()
		_ = os.Remove(u.file.Name())
	}
	u.chunks = nil
}

// errNeedsFile reports that a body cannot be kept in memory, or no longer.
var errNeedsFile = errors.New("the body goes to a temporary file")

// Sizes of the pieces a body in memory is read into.
const (
	minBodyChunk   = 512
	firstBodyChunk = 4 << 10  // of a body of unknown length
	mostFirstChunk = 16 << 10 // of a body whose length is declared
)

// nextChunk is the size of the next piece for a body in memory of which
// total bytes arrived: as much as arrived so far, so that a body holds at
// most about twice what it sent, or its first piece, and never more than a
// small body is. A
// request that declares its length (hint, negative when it declares none)
// gets no more than what is left of it and one byte, which is where the
// read that finds the end of the body has its room.
func nextChunk(total int, hint int64) int {
	size := total
	switch {
	case total == 0 && hint < 0:
		size = firstBodyChunk
	case total == 0:
		size = int(min(hint+1, mostFirstChunk))
	case hint >= int64(total):
		size = int(min(int64(size), hint-int64(total)+1))
	}
	return min(max(size, minBodyChunk), smallRequestSize-total)
}

// readMemory reads the body into memory, in pieces whose room is reserved
// in bodyMemory before they are made; the bytes that arrive are reserved in
// MaxSpoolSize as in a file. It returns errNeedsFile when the body is not
// small after all or bodyMemory has no room for its next piece: what
// arrived so far is in u.chunks, and the rest is still to be read.
func (u *upload) readMemory(in io.Reader, hint int64, res *spoolReservation) error {
	total, free := 0, 0
	for {
		if free == 0 {
			if total >= smallRequestSize {
				return errNeedsFile
			}
			free = nextChunk(total, hint)
			if !res.growMemory(int64(free)) {
				return errNeedsFile
			}
			u.chunks = append(u.chunks, make([]byte, 0, free))
		}
		last := &u.chunks[len(u.chunks)-1]
		n, err := in.Read((*last)[len(*last):cap(*last)])
		*last = (*last)[:len(*last)+n]
		total, free = total+n, free-n
		u.size = int64(total)
		if n > 0 {
			if err := res.grow(u.size); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// readFile writes what arrived in memory, and then the rest of the body, to
// a temporary file, and gives the memory back.
func (u *upload) readFile(in io.Reader, res *spoolReservation) error {
	f, err := os.CreateTemp("", "qti-upload-*")
	if err != nil {
		return err
	}
	u.file = f
	out := &reservingWriter{w: f, res: res}
	for _, c := range u.chunks {
		if _, err := out.Write(c); err != nil {
			return err
		}
	}
	u.chunks = nil
	res.releaseMemory()
	_, err = io.Copy(out, in)
	u.size = out.written
	return err
}

// spool receives the input in full before validation starts: a ZIP must be
// read from the end, and a slow upload must not hold a validation slot. A
// small body, under smallRequestSize, is kept in memory while bodyMemory
// has room for it; any other goes to a temporary file, as does one that
// started in memory and outgrew it. hint is the length of the input when
// the request declares it, negative when it does not. More than limit bytes
// is refused as too large. The room of the body in MaxSpoolSize is reserved
// in res as the bytes arrive, wherever they are kept; an input that does
// not fit gets 503, or 413 when it would not fit in the whole budget. The
// caller closes the upload and releases res. On failure the response has
// been written.
func (s *Server) spool(w http.ResponseWriter, body io.Reader, limit, hint int64, res *spoolReservation) (*upload, bool) {
	in := &recordingReader{r: io.LimitReader(body, limit+1)}
	u := &upload{}
	err := errNeedsFile
	if hint < smallRequestSize {
		err = u.readMemory(in, hint, res)
	}
	if errors.Is(err, errNeedsFile) {
		err = u.readFile(in, res)
		if u.file == nil {
			res.releaseMemory()
			s.log.Error("create temp file", "error", err)
			writeError(w, http.StatusInternalServerError, string(qti.CodeInternal), "cannot store the upload")
			return nil, false
		}
	}
	if err == nil && u.size > limit {
		err = &http.MaxBytesError{Limit: limit}
	}
	if err == nil {
		res.shrink(u.size)
		return u, true
	}
	u.close()
	res.releaseMemory()
	switch {
	case errors.Is(err, errSpoolBusy):
		refuseUnread(w)
		writeBusy(w, "too many uploads in progress")
	case errors.Is(err, errSpoolTooLarge):
		refuseUnread(w)
		writeSpoolTooLarge(w)
	case in.err == nil && !isTooLarge(err):
		// Not the body but the file failed, as when /tmp is full.
		s.log.Error("write temp file", "error", err)
		writeError(w, http.StatusInternalServerError, string(qti.CodeInternal), "cannot store the upload")
	default:
		writeReadError(w, err, "package is larger than the configured MAX_PACKAGE_SIZE")
	}
	return nil, false
}

// reservingWriter reserves room in res for the bytes before it writes them.
type reservingWriter struct {
	w       io.Writer
	res     *spoolReservation
	written int64
}

func (rw *reservingWriter) Write(p []byte) (int, error) {
	if err := rw.res.grow(rw.written + int64(len(p))); err != nil {
		return 0, err
	}
	n, err := rw.w.Write(p)
	rw.written += int64(n)
	return n, err
}

// recordingReader keeps the error of reading r, to tell it from an error of
// writing what was read.
type recordingReader struct {
	r   io.Reader
	err error
}

func (rr *recordingReader) Read(p []byte) (int, error) {
	n, err := rr.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		rr.err = err
	}
	return n, err
}

// writeReadError answers a request whose body could not be read: too large,
// not received in time, or malformed.
func writeReadError(w http.ResponseWriter, err error, tooLarge string) {
	switch {
	case isTooLarge(err):
		writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), tooLarge)
	case isTimeout(err):
		writeError(w, http.StatusRequestTimeout, codeRequestTimeout, "the request body did not arrive within REQUEST_TIMEOUT")
	default:
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "cannot read the request body")
	}
}

func isTooLarge(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

// isTimeout reports whether a read failed because its deadline passed.
func isTimeout(err error) bool {
	if errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	ne, ok := errors.AsType[net.Error](err)
	return ok && ne.Timeout()
}

func goVersion() string { return runtime.Version() }
