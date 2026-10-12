package httpapi

import (
	"fmt"
	"net/http"
	"runtime/debug"
	"time"

	"qti3-validator/internal/domain/qti"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Unwrap lets http.ResponseController reach the connection.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// logRequests logs method, path, status and duration, and turns a panic into
// a 500: the panic and its stack go to the log, at error level with the
// request, and the client gets no more than "internal error". Bodies are
// never logged. http.ErrAbortHandler is not an error but the way to abort a
// response: it is passed on to the server, which closes the connection.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			p := recover()
			if p != nil && p != http.ErrAbortHandler { //nolint:errorlint // the sentinel itself is what aborts
				s.log.Error("panic", "method", r.Method, "path", r.URL.Path, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				rec.status = http.StatusInternalServerError // also when the response had begun
				writeError(rec, http.StatusInternalServerError, string(qti.CodeInternal), "internal error")
			}
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes_in", r.ContentLength, "duration_ms", time.Since(start).Milliseconds())
			if p != nil && p == http.ErrAbortHandler { //nolint:errorlint // as above
				panic(p)
			}
		}()
		next.ServeHTTP(rec, r)
	})
}
