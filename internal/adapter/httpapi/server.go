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
	"math"
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
	// MaxValidationMemory bounds the heap the running validations may hold at
	// once, in bytes. A validation reserves an estimate of its peak heap
	// (estimateMemory) from it, so the memory of the documents being
	// validated fits the container however large MaxConcurrent is. 0 disables
	// the bound: concurrency is then limited only by MaxConcurrent.
	MaxValidationMemory int64
	RequestTimeout      time.Duration
}

// Per-document heap, as a function of the document size, for reserving room
// in MaxValidationMemory. The XPath DOM the Schematron rules build dominates
// the peak: a Node is about 250 bytes, and the densest well-formed markup
// (single-character elements with single-character text, e.g. "<b/>x"
// repeated) reaches one node per ~2.5 bytes, so the live DOM alone is about
// 100 bytes of heap per byte of document. Measured peak during a full
// validation (XML Schema check, DOM, rules and report) of such content is
// about 112×; validationBytesPerByte rounds that up for margin. Typical QTI
// content is far lower (13–27×), so the estimate is a safe upper bound, not
// a typical figure. See docs/deployment.md and internal/bootstrap's
// TestMeasureMemory.
const (
	validationBytesPerByte = 128
	validationMemoryBase   = 4 << 20 // transient allocations independent of size
)

// estimateMemory estimates the peak heap a validation of a body of the given
// size and shape needs. A package is validated one entry at a time, so its
// peak is driven by the largest entry it may contain, which maxFileSize
// bounds; a single document by its own size.
func estimateMemory(size, maxFileSize int64, pkg bool) int64 {
	n := size
	if pkg {
		n = maxFileSize
	}
	// Guard the multiplication against overflow; sizes are already bounded by
	// the request limits, so this only matters for absurd configuration.
	if n > (math.MaxInt64-validationMemoryBase)/validationBytesPerByte {
		return math.MaxInt64
	}
	return validationMemoryBase + n*validationBytesPerByte
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
	waiting   atomic.Int64 // requests waiting for a slot
	log       *slog.Logger

	spoolMu   sync.Mutex
	spoolUsed int64 // bytes reserved by requests for their temporary files

	mem *memSem // heap budget for the running validations; nil when disabled
	// maxItem is the largest single document or package entry whose estimated
	// peak heap fits the whole validation-memory budget. A larger document is
	// refused with 413; a larger package entry is reported too large, so the
	// package is still validated. math.MaxInt64 when the budget is disabled.
	maxItem int64

	writeGrace time.Duration
}

// memSem is a weighted semaphore in bytes: the heap the running validations
// may hold at once. A validation reserves its estimated peak, so the memory
// of the documents being validated stays within the budget whatever
// MaxConcurrent is. It never deadlocks: a reservation larger than the whole
// budget is admitted once nothing else holds any, so a single large document
// still runs (bounded alone), while two never run together.
type memSem struct {
	mu     sync.Mutex
	budget int64
	used   int64
	// released is closed and replaced on every release, to wake waiters.
	released chan struct{}
}

func newMemSem(budget int64) *memSem {
	if budget <= 0 {
		return nil
	}
	return &memSem{budget: budget, released: make(chan struct{})}
}

// tryReserve reserves n bytes without waiting. It succeeds when n fits
// beside what is used, or when nothing is used (so a reservation larger than
// the whole budget still gets in, alone). It returns the channel to wait on
// otherwise.
func (m *memSem) tryReserve(n int64) (bool, chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.used == 0 || n <= m.budget-m.used {
		m.used += n
		return true, nil
	}
	return false, m.released
}

func (m *memSem) release(n int64) {
	m.mu.Lock()
	m.used -= n
	close(m.released)
	m.released = make(chan struct{})
	m.mu.Unlock()
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
	maxItem := int64(math.MaxInt64)
	if opts.MaxValidationMemory > 0 {
		// The largest item whose estimate fits the whole budget; at least 1.
		maxItem = max((opts.MaxValidationMemory-validationMemoryBase)/validationBytesPerByte, 1)
	}
	return &Server{
		opts: opts, validator: v, slots: make(chan struct{}, opts.MaxConcurrent), log: log,
		mem:        newMemSem(opts.MaxValidationMemory),
		maxItem:    maxItem,
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

// admit reserves the memory the validation needs and then a CPU slot, in
// that order: a slot is held only by a validation that already has its
// memory and so is making progress, which keeps the memory reservation from
// deadlocking. estimate is the heap the validation is expected to hold; it
// is released, with the slot, by the returned function. On refusal the
// response has been written (503, or 413 when the request can never fit).
func (s *Server) admit(ctx context.Context, estimate int64, w http.ResponseWriter) (func(), bool) {
	releaseMem, ok := s.acquireMemory(ctx, estimate, w)
	if !ok {
		return nil, false
	}
	releaseSlot, ok := s.acquire(ctx, w)
	if !ok {
		releaseMem()
		return nil, false
	}
	return func() { releaseSlot(); releaseMem() }, true
}

// acquireMemory reserves estimate bytes of the validation-memory budget,
// waiting for room like acquire waits for a slot: at most MaxQueue requests
// wait, and a request that gets no room before ctx is done gets 503. A
// reservation is admitted at once when nothing else holds any memory, so a
// single document larger than the whole budget still runs, alone. When the
// budget is disabled it reserves nothing.
func (s *Server) acquireMemory(ctx context.Context, estimate int64, w http.ResponseWriter) (func(), bool) {
	if s.mem == nil {
		return func() {}, true
	}
	if ok, _ := s.mem.tryReserve(estimate); ok {
		return func() { s.mem.release(estimate) }, true
	}
	if s.waiting.Add(1) > int64(s.opts.MaxQueue) {
		s.waiting.Add(-1)
		writeBusy(w, "the validations in progress fill MAX_VALIDATION_MEMORY")
		return nil, false
	}
	defer s.waiting.Add(-1)
	for {
		ok, released := s.mem.tryReserve(estimate)
		if ok {
			return func() { s.mem.release(estimate) }, true
		}
		select {
		case <-released:
		case <-ctx.Done():
			writeBusy(w, "no validation memory available in time")
			return nil, false
		}
	}
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
