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
	// MaxValidationMemory is the memory the validations in progress may
	// reserve together, in bytes: each reserves what its document takes
	// (app.Memory), and waits for room when there is none. 0 sets no bound:
	// only MaxConcurrent then limits them.
	MaxValidationMemory int64
	RequestTimeout      time.Duration
}

// writeGrace is the time a response gets to be written once validation has
// returned. Validation may return after REQUEST_TIMEOUT: the work in progress
// when time runs out, such as the XML Schema check of the current document,
// cannot be interrupted.
const writeGrace = 10 * time.Second

// retryAfter is the Retry-After of a busy response, in seconds.
const retryAfter = "5"

// Server handles the HTTP API.
type Server struct {
	opts      Options
	validator Validator
	slots     chan struct{}
	// largeSlots bounds the large requests among the slots; nil with one
	// slot, which every request may take. See tryTakeSlot.
	largeSlots chan struct{}
	waiting    atomic.Int64 // requests waiting for a slot
	log        *slog.Logger

	spoolMu   sync.Mutex
	spoolUsed int64 // bytes reserved by requests for their temporary files

	mem *memSem // the memory of the validations in progress; nil without a bound

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
	var largeSlots chan struct{}
	if opts.MaxConcurrent > 1 {
		largeSlots = make(chan struct{}, opts.MaxConcurrent-1)
	}
	return &Server{
		opts: opts, validator: v, slots: make(chan struct{}, opts.MaxConcurrent), largeSlots: largeSlots, log: log,
		mem:        newMemSem(opts.MaxValidationMemory),
		writeGrace: writeGrace,
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

// smallRequestSize is the size of a request body below which a request is
// small: a typical item, validated in milliseconds.
const smallRequestSize = 256 << 10

// acquire waits for a validation slot, bounding concurrent work. At most
// MaxQueue requests wait, in about the order they came; beyond that, and for
// requests that cannot get a slot before ctx is done, the answer is 503.
func (s *Server) acquire(ctx context.Context, w http.ResponseWriter, large bool) bool {
	if s.tryTakeSlot(large) {
		return true
	}
	if s.waiting.Add(1) > int64(s.opts.MaxQueue) {
		s.waiting.Add(-1)
		writeBusy(w, "too many requests are waiting for a validation slot")
		return false
	}
	defer s.waiting.Add(-1)
	if !s.takeSlot(ctx, large) {
		writeBusy(w, "no validation slot available in time")
		return false
	}
	return true
}

// A slot lets one validation run. With two or more slots, one is kept for
// small requests: a large request, of smallRequestSize or more, needs a
// place among the large ones as well, of which there is one less than there
// are slots. So a typical item is not kept waiting for seconds by large
// documents that take every slot.

// tryTakeSlot takes a slot if one is free.
func (s *Server) tryTakeSlot(large bool) bool {
	if large && s.largeSlots != nil {
		select {
		case s.largeSlots <- struct{}{}:
		default:
			return false
		}
	}
	select {
	case s.slots <- struct{}{}:
		return true
	default:
	}
	if large && s.largeSlots != nil {
		<-s.largeSlots
	}
	return false
}

// takeSlot waits for a slot until ctx is done, and reports whether it got
// one.
func (s *Server) takeSlot(ctx context.Context, large bool) bool {
	if large && s.largeSlots != nil {
		select {
		case s.largeSlots <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}
	select {
	case s.slots <- struct{}{}:
		return true
	case <-ctx.Done():
	}
	if large && s.largeSlots != nil {
		<-s.largeSlots
	}
	return false
}

// freeSlot gives a slot back.
func (s *Server) freeSlot(large bool) {
	<-s.slots
	if large && s.largeSlots != nil {
		<-s.largeSlots
	}
}

// Errors of growing a spoolReservation.
var (
	errSpoolBusy     = errors.New("the uploads in progress fill MAX_SPOOL_SIZE")
	errSpoolTooLarge = errors.New("the upload is larger than MAX_SPOOL_SIZE")
)

// spoolReservation is the room in MaxSpoolSize one request holds for its
// temporary file. It grows with the bytes that arrive, so a stalled upload
// holds only what it has sent, and filling the budget takes sending that
// much; release frees it.
type spoolReservation struct {
	s *Server
	n int64 // bytes reserved
}

func (s *Server) newSpoolReservation() *spoolReservation {
	return &spoolReservation{s: s}
}

// grow makes room for need bytes in all. A request that does not fit gets
// errSpoolBusy at once: waiting would hold its connection without bounding
// anything. need beyond the whole budget is errSpoolTooLarge.
func (r *spoolReservation) grow(need int64) error {
	if need <= r.n {
		return nil
	}
	s := r.s
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	if need > s.opts.MaxSpoolSize {
		return errSpoolTooLarge
	}
	if need-r.n > s.opts.MaxSpoolSize-s.spoolUsed {
		return errSpoolBusy
	}
	s.spoolUsed += need - r.n
	r.n = need
	return nil
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
