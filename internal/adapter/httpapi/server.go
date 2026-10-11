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
	MaxConcurrent   int   // validations running at once, beside one small one on the express slot
	MaxQueue        int   // requests waiting for a slot; 0 selects 4 × MaxConcurrent
	MaxSpoolSize    int64 // request bodies held at once, in memory and in temporary files, bytes; 0 selects 4 × MaxPackageSize
	// MaxValidationMemory is the memory the validations in progress may
	// reserve together, in bytes: each reserves what its document takes
	// (app.Memory), and waits for room when there is none. A share of it is
	// kept for small validations (SmallMemoryShare). 0 sets no bound: only
	// MaxConcurrent then limits them.
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
	// slots are the MaxConcurrent slots any request may take; express is
	// one more, for small requests only. See takeSlot.
	slots   chan struct{}
	express chan struct{}
	// The requests that wait, for a slot or for memory: the large ones and
	// the small ones apart, each at most MaxQueue, so that large documents
	// that queue up do not turn typical items away. See queue.
	waiting      atomic.Int64
	waitingSmall atomic.Int64
	log          *slog.Logger

	spoolMu   sync.Mutex
	spoolUsed int64 // bytes reserved by requests for their bodies, wherever those are kept
	// bodyMemory is the room for the bodies that are kept in memory, of all
	// requests together, and bodyMemoryUsed what is reserved of it. A body
	// that finds no room there goes to a temporary file.
	bodyMemory     int64
	bodyMemoryUsed int64

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
	return &Server{
		opts: opts, validator: v, log: log,
		slots: make(chan struct{}, opts.MaxConcurrent), express: make(chan struct{}, 1),
		bodyMemory: BodyMemory(opts.MaxValidationMemory),
		mem:        newMemSem(opts.MaxValidationMemory, opts.MaxConcurrent),
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
// small: a typical item, validated in milliseconds. A small body is kept in
// memory rather than in a temporary file, and a small request may take the
// express slot.
const smallRequestSize = 256 << 10

// BodyMemory is the room for the request bodies that are kept in memory,
// with a budget of MaxValidationMemory: a thirty-second of it, at least
// 4 MiB and at most 32 MiB, and 32 MiB without a budget. It comes on top of
// that budget, out of what the process has to spare.
func BodyMemory(budget int64) int64 {
	const least, most = 4 << 20, 32 << 20
	if budget <= 0 {
		return most
	}
	return min(max(budget/32, least), most)
}

// A slot lets one validation run. There are MaxConcurrent regular slots,
// which any request may take, and one express slot more, which only a small
// request may take: one with a body under smallRequestSize whose validation
// reserves no more than smallReservation. So a typical item is not kept
// waiting for seconds by large documents that take every regular slot, and
// those run MaxConcurrent at a time. A small validation takes about a
// millisecond of CPU, so the one that runs beside MaxConcurrent others
// costs them little. It has no CPU of its own, though: while MaxConcurrent
// validations keep every CPU busy, it waits for the runtime to hand one
// over, which takes milliseconds.
type slotKind int

const (
	noSlot slotKind = iota
	regularSlot
	expressSlot
)

// acquire waits for a validation slot, bounding concurrent work. At most
// MaxQueue requests of a kind wait, in about the order they came; beyond
// that, and for requests that cannot get a slot before ctx is done, the
// answer is 503 and the result noSlot.
func (s *Server) acquire(ctx context.Context, w http.ResponseWriter, large bool) slotKind {
	if slot := s.tryTakeSlot(large); slot != noSlot {
		return slot
	}
	waiting := s.queue(large)
	if waiting.Add(1) > int64(s.opts.MaxQueue) {
		waiting.Add(-1)
		writeBusy(w, "too many requests are waiting for a validation slot")
		return noSlot
	}
	defer waiting.Add(-1)
	slot := s.takeSlot(ctx, large)
	if slot == noSlot {
		writeBusy(w, "no validation slot available in time")
	}
	return slot
}

// queue is the count of the requests of a kind that wait.
func (s *Server) queue(large bool) *atomic.Int64 {
	if large {
		return &s.waiting
	}
	return &s.waitingSmall
}

// tryTakeRegular takes a regular slot if one is free.
func (s *Server) tryTakeRegular() bool {
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// tryTakeSlot takes a slot if one is free. A small request takes the
// express slot first, which leaves the regular ones to the requests that
// have no other.
func (s *Server) tryTakeSlot(large bool) slotKind {
	if !large {
		select {
		case s.express <- struct{}{}:
			return expressSlot
		default:
		}
	}
	if s.tryTakeRegular() {
		return regularSlot
	}
	return noSlot
}

// takeSlot waits for a slot until ctx is done: a large request for a
// regular one, a small request for whichever comes free first.
func (s *Server) takeSlot(ctx context.Context, large bool) slotKind {
	express := s.express
	if large {
		express = nil // never ready
	}
	select {
	case express <- struct{}{}:
		return expressSlot
	case s.slots <- struct{}{}:
		return regularSlot
	case <-ctx.Done():
		return noSlot
	}
}

// freeSlot gives a slot back.
func (s *Server) freeSlot(slot slotKind) {
	switch slot {
	case regularSlot:
		<-s.slots
	case expressSlot:
		<-s.express
	case noSlot:
	}
}

// Errors of growing a spoolReservation.
var (
	errSpoolBusy     = errors.New("the uploads in progress fill MAX_SPOOL_SIZE")
	errSpoolTooLarge = errors.New("the upload is larger than MAX_SPOOL_SIZE")
)

// spoolReservation is the room in MaxSpoolSize one request holds for its
// body, in memory or in a temporary file. It grows with the bytes that
// arrive, so a stalled upload holds only what it has sent, and filling the
// budget takes sending that much. A body in memory also holds room in
// bodyMemory, for the memory it is read into. release frees both.
type spoolReservation struct {
	s   *Server
	n   int64 // bytes reserved in MaxSpoolSize
	mem int64 // bytes reserved in bodyMemory
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

// growMemory reserves n bytes more in bodyMemory, if there is room.
func (r *spoolReservation) growMemory(n int64) bool {
	s := r.s
	s.spoolMu.Lock()
	defer s.spoolMu.Unlock()
	if n > s.bodyMemory-s.bodyMemoryUsed {
		return false
	}
	s.bodyMemoryUsed += n
	r.mem += n
	return true
}

// releaseMemory gives back what is reserved in bodyMemory.
func (r *spoolReservation) releaseMemory() {
	if r.mem == 0 {
		return
	}
	r.s.spoolMu.Lock()
	r.s.bodyMemoryUsed -= r.mem
	r.s.spoolMu.Unlock()
	r.mem = 0
}

// release frees the reservation.
func (r *spoolReservation) release() {
	r.shrink(0)
	r.releaseMemory()
}

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
