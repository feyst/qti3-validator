// Package httpapi is the HTTP adapter: it exposes the validation use cases
// as a JSON API and answers with the report of package report.
//
// Two request shapes are served. /v1/validate and /v1/validate/package take
// the document or package as the request body; /api/validate takes it as a
// multipart upload.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// Name and Version identify the service in /version and in reports. Version
// is set at build time with -ldflags "-X .../httpapi.Version=...".
const Name = "qti-validator"

// Version is the service version.
var Version = "0.1.0"

// Validator is the application port this adapter drives.
type Validator interface {
	ValidateDocument(ctx context.Context, q app.ValidateDocument) qti.DocumentResult
	ValidatePackage(ctx context.Context, q app.ValidatePackage) qti.PackageResult
}

// Options configure the server.
type Options struct {
	Addr           string
	MaxPackageSize int64 // body of /v1/validate/package and upload of /api/validate, bytes
	PackageLimits  qti.PackageLimits
	MaxConcurrent  int // validations running at once
	RequestTimeout time.Duration
}

// Server handles the HTTP API.
type Server struct {
	opts      Options
	validator Validator
	slots     chan struct{}
	log       *slog.Logger
}

// New returns a server that validates with v.
func New(opts Options, v Validator, log *slog.Logger) *Server {
	return &Server{opts: opts, validator: v, slots: make(chan struct{}, opts.MaxConcurrent), log: log}
}

// Handler returns the routes, wrapped in request logging.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("POST /v1/validate", s.validateDocument)
	mux.HandleFunc("POST /v1/validate/package", s.validatePackage)
	mux.HandleFunc("GET /api/validators", s.apiValidators)
	mux.HandleFunc("POST /api/validate", s.apiValidate)
	return s.logRequests(mux)
}

// HTTPServer returns an http.Server with timeouts derived from the options.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       s.opts.RequestTimeout,
		WriteTimeout:      s.opts.RequestTimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    64 << 10,
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"name":                Name,
		"version":             Version,
		"go_version":          goVersion(),
		"qti_versions":        qti.SupportedVersions(),
		"default_qti_version": qti.LatestVersion().Name,
	})
}

// acquire waits for a validation slot, bounding concurrent work and with it
// peak memory. Requests that cannot get a slot before their timeout get 503.
func (s *Server) acquire(w http.ResponseWriter, r *http.Request) (context.Context, func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.RequestTimeout)
	select {
	case s.slots <- struct{}{}:
		return ctx, func() { <-s.slots; cancel() }, true
	case <-ctx.Done():
		cancel()
		writeError(w, http.StatusServiceUnavailable, codeBusy, "no validation slot available in time")
		return nil, nil, false
	}
}
