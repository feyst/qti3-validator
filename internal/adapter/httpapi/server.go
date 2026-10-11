// Package httpapi is the HTTP adapter: it exposes the validation use cases
// as a JSON API and answers with the report of package report.
//
// Validation is one endpoint, POST /api/validate. It takes the document or
// package as the request body or as a multipart upload.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
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
	Addr            string
	MaxPackageSize  int64 // request body of /api/validate, bytes
	MaxDocumentSize int64 // one XML document sent as such, bytes; 0 selects the default
	PackageLimits   qti.PackageLimits
	MaxConcurrent   int   // validations running at once
	MaxQueue        int   // requests waiting for a slot; 0 selects 4 × MaxConcurrent
	MaxSpoolSize    int64 // request bodies in temporary files at once, bytes; 0 selects 4 × MaxPackageSize
	RequestTimeout  time.Duration
}

// writeGrace is the time a response gets to be written once validation has
// returned. Validation may return after REQUEST_TIMEOUT: the work in progress
// when time runs out, such as the XML Schema check of the current document,
// cannot be interrupted.
const writeGrace = 10 * time.Second

// retryAfter is the Retry-After of a busy response, in seconds.
const retryAfter = "5"

// defaultSpoolChunk is the room in MaxSpoolSize an upload reserves at a time,
// as its body arrives.
const defaultSpoolChunk = 1 << 20

// Server handles the HTTP API.
type Server struct {
	opts      Options
	validator Validator
	slots     chan struct{}
	waiting   atomic.Int64 // requests waiting for a slot
	log       *slog.Logger

	spoolMu    sync.Mutex
	spoolUsed  int64 // bytes reserved by requests for their temporary files
	spoolChunk int64 // bytes an upload reserves at a time as its body arrives

	writeGrace time.Duration
}

// New returns a server that validates with v.
func New(opts Options, v Validator, log *slog.Logger) *Server {
	opts.MaxDocumentSize = qti.Limits{MaxDocumentSize: opts.MaxDocumentSize}.WithDefaults().MaxDocumentSize
	if opts.MaxQueue <= 0 {
		opts.MaxQueue = 4 * opts.MaxConcurrent
	}
	if opts.MaxSpoolSize <= 0 {
		opts.MaxSpoolSize = 4 * opts.MaxPackageSize
	}
	return &Server{
		opts: opts, validator: v, slots: make(chan struct{}, opts.MaxConcurrent), log: log,
		writeGrace: writeGrace, spoolChunk: defaultSpoolChunk,
	}
}

// Handler returns the routes, wrapped in request logging.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /api/validators", s.apiValidators)
	mux.HandleFunc("POST /api/validate", s.apiValidate)
	return s.logRequests(mux)
}

// HTTPServer returns an http.Server with timeouts derived from the options.
// POST /api/validate sets its own deadlines; WriteTimeout covers the
// longest it allows, a response written writeGrace after REQUEST_TIMEOUT.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.opts.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       s.opts.RequestTimeout,
		WriteTimeout:      s.opts.RequestTimeout + s.writeGrace,
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
// peak memory. At most MaxQueue requests wait, in about the order they
// came; beyond that, and for requests that cannot get a slot before ctx is
// done, the answer is 503.
func (s *Server) acquire(ctx context.Context, w http.ResponseWriter) (func(), bool) {
	release := func() { <-s.slots }
	select {
	case s.slots <- struct{}{}:
		return release, true
	default:
	}
	if s.waiting.Add(1) > int64(s.opts.MaxQueue) {
		s.waiting.Add(-1)
		writeBusy(w, "too many requests are waiting for a validation slot")
		return nil, false
	}
	defer s.waiting.Add(-1)
	select {
	case s.slots <- struct{}{}:
		return release, true
	case <-ctx.Done():
		writeBusy(w, "no validation slot available in time")
		return nil, false
	}
}

// Errors of growing a spoolReservation.
var (
	errSpoolBusy     = errors.New("the uploads in progress fill MAX_SPOOL_SIZE")
	errSpoolTooLarge = errors.New("the upload is larger than MAX_SPOOL_SIZE")
)

// spoolReservation is the room in MaxSpoolSize one request holds for its
// temporary file. It grows as the body arrives, so a stalled upload holds
// only what it has sent; release frees it.
type spoolReservation struct {
	s    *Server
	most int64 // the most the body may take, where the last chunk ends
	n    int64 // bytes reserved
}

func (s *Server) newSpoolReservation(most int64) *spoolReservation {
	return &spoolReservation{s: s, most: most}
}

// grow makes room for need bytes in all. It reserves whole chunks, up to the
// most the body may take, and when that does not fit, only need. A
// request that does not fit gets errSpoolBusy at once: waiting would hold
// its connection without bounding anything. need beyond the whole budget
// is errSpoolTooLarge.
func (r *spoolReservation) grow(need int64) error {
	if need <= r.n {
		return nil
	}
	s := r.s
	// need rounded up to whole chunks, the last one cut at most.
	ahead := max(need, min((need+s.spoolChunk-1)/s.spoolChunk*s.spoolChunk, r.most))
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	if need > s.opts.MaxSpoolSize {
		return errSpoolTooLarge
	}
	free := s.opts.MaxSpoolSize - s.spoolUsed
	for _, want := range []int64{min(ahead, s.opts.MaxSpoolSize), need} {
		if want-r.n <= free {
			s.spoolUsed += want - r.n
			r.n = want
			return nil
		}
	}
	return errSpoolBusy
}

// shrink gives back what is reserved beyond n bytes.
func (r *spoolReservation) shrink(n int64) {
	if n >= r.n {
		return
	}
	r.s.spoolMu.Lock()
	r.s.spoolUsed -= r.n - n
	r.s.spoolMu.Unlock()
	r.n = n
}

// release frees the reservation.
func (r *spoolReservation) release() { r.shrink(0) }

// refuseUnread closes the connection after a response written before the
// body was read. Otherwise the server reads up to 256 KiB of the body before
// it answers, to reuse the connection.
func refuseUnread(w http.ResponseWriter) {
	w.Header().Set("Connection", "close")
}

// writeBusy answers 503: the service has no room for the request now.
func writeBusy(w http.ResponseWriter, message string) {
	w.Header().Set("Retry-After", retryAfter)
	writeError(w, http.StatusServiceUnavailable, codeBusy, message+"; retry later")
}
