package httpapi

import (
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

// spool copies the input to a temporary file: a ZIP must be read from the
// end, and the whole input is received before validation starts. More than
// limit bytes is refused as too large. The caller removes the file. On
// failure the response has been written.
func (s *Server) spool(w http.ResponseWriter, body io.Reader, limit int64) (*os.File, int64, bool) {
	f, err := os.CreateTemp("", "qti-upload-*")
	if err != nil {
		s.log.Error("create temp file", "error", err)
		writeError(w, http.StatusInternalServerError, string(qti.CodeInternal), "cannot store the upload")
		return nil, 0, false
	}
	in := &recordingReader{r: io.LimitReader(body, limit+1)}
	size, err := io.Copy(f, in)
	if err == nil && size > limit {
		err = &http.MaxBytesError{Limit: limit}
	}
	if err == nil {
		return f, size, true
	}
	_ = f.Close()
	_ = os.Remove(f.Name())
	if in.err == nil && !isTooLarge(err) {
		// Not the body but the file failed, as when /tmp is full.
		s.log.Error("write temp file", "error", err)
		writeError(w, http.StatusInternalServerError, string(qti.CodeInternal), "cannot store the upload")
		return nil, 0, false
	}
	writeReadError(w, err, "package is larger than the configured MAX_PACKAGE_SIZE")
	return nil, 0, false
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
