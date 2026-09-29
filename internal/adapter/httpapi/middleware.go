package httpapi

import (
	"net/http"
	"time"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// logRequests logs method, path, status and duration, and turns a panic into
// a 500. Bodies are never logged.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", p)
				writeError(rec, http.StatusInternalServerError, string(qti.CodeInternal), "internal error")
			}
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes_in", r.ContentLength, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}
