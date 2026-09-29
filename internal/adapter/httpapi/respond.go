package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"runtime"
	"strings"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// Codes of request errors: the request could not be validated at all. Codes
// of findings are in package qti.
const (
	codeUnsupportedMediaType = "unsupported_media_type"
	codeInvalidRequest       = "invalid_request"
	codeUnknownValidator     = "unknown_validator"
	codeBusy                 = "busy"
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
// verdict is summary.outcome. Only a failure of the service itself is 500.
func statusFor(o qti.Outcome) int {
	if o == qti.OutcomeInternal {
		return http.StatusInternalServerError
	}
	return http.StatusOK
}

func hasMediaType(r *http.Request, ok func(string) bool) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && ok(mt)
}

func isXMLMediaType(mt string) bool {
	return mt == "application/xml" || mt == "text/xml" || strings.HasSuffix(mt, "+xml")
}

func isZIPMediaType(mt string) bool {
	return mt == "application/zip" || mt == "application/x-zip-compressed" || mt == "application/octet-stream"
}

// spool copies a package to a temporary file, because a ZIP must be read
// from the end. The caller removes the file. On failure the response has
// been written.
func (s *Server) spool(w http.ResponseWriter, body io.Reader) (*os.File, int64, bool) {
	f, err := os.CreateTemp("", "qti-package-*.zip")
	if err != nil {
		s.log.Error("create temp file", "error", err)
		writeError(w, http.StatusInternalServerError, string(qti.CodeInternal), "cannot store the package")
		return nil, 0, false
	}
	size, err := io.Copy(f, io.LimitReader(body, s.opts.MaxPackageSize+1))
	if err == nil && size > s.opts.MaxPackageSize {
		err = &http.MaxBytesError{Limit: s.opts.MaxPackageSize}
	}
	if err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), "package is larger than the configured MAX_PACKAGE_SIZE")
			return nil, 0, false
		}
		writeError(w, http.StatusBadRequest, codeInvalidRequest, "cannot read the request body")
		return nil, 0, false
	}
	return f, size, true
}

func isTooLarge(err error) bool {
	_, ok := errors.AsType[*http.MaxBytesError](err)
	return ok
}

func goVersion() string { return runtime.Version() }
