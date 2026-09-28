// Package server exposes the validator over HTTP.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/kennisnet/qti3-validator/internal/validator"
)

// Name and Version identify the service in /version. Version is set at
// build time with -ldflags "-X .../internal/server.Version=...".
const Name = "qti-validator"

var Version = "0.1.0"

// Server handles the HTTP API.
type Server struct {
	cfg       Config
	validator *validator.Validator
	slots     chan struct{}
	log       *slog.Logger
}

// New returns a server that validates with v.
func New(cfg Config, v *validator.Validator, log *slog.Logger) *Server {
	return &Server{cfg: cfg, validator: v, slots: make(chan struct{}, cfg.MaxConcurrent), log: log}
}

// Handler returns the routes, wrapped in request logging.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("POST /v1/validate", s.validate)
	mux.HandleFunc("POST /v1/validate/package", s.validatePackage)
	mux.HandleFunc("GET /api/validators", s.apiValidators)
	mux.HandleFunc("POST /api/validate", s.apiValidate)
	return s.logRequests(mux)
}

// HTTPServer returns an http.Server with timeouts derived from the config.
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       s.cfg.RequestTimeout,
		WriteTimeout:      s.cfg.RequestTimeout + 5*time.Second,
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
		"go_version":          runtime.Version(),
		"qti_versions":        validator.SupportedVersions(),
		"default_qti_version": validator.LatestVersion(),
	})
}

func (s *Server) validate(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r, isXMLMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/xml")
		return
	}
	opts, ok := validateOptions(w, r)
	if !ok {
		return
	}
	ctx, release, ok := s.acquire(w, r)
	if !ok {
		return
	}
	defer release()

	res := s.validator.Validate(ctx, r.Body, opts)
	if res.Outcome == validator.OutcomeTooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, validator.CodeTooLarge, res.Errors[0].Message)
		return
	}
	writeJSON(w, statusFor(res.Outcome), validator.DocumentReport(res, s.reportMeta(r, "document.xml")))
}

func (s *Server) validatePackage(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r, isZIPMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/zip")
		return
	}
	opts, ok := validateOptions(w, r)
	if !ok {
		return
	}
	ctx, release, ok := s.acquire(w, r)
	if !ok {
		return
	}
	defer release()

	f, size, ok := s.spool(w, r.Body)
	if !ok {
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	s.writePackageReport(ctx, w, f, size, opts, s.reportMeta(r, "package.zip"))
}

// apiValidate serves POST /api/validate?validatorId=… with the input in the
// multipart field "file", answered with HTTP 200 and a Report, and 400 for
// an unknown validatorId. The request shape follows the public OpenAPI at
// https://vc.1ed.tech/v3/api-docs.
// The file may be a package (ZIP) or a single XML document.
func (s *Server) apiValidate(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("validatorId"); id != "" && !slices.ContainsFunc(Validators, func(v ValidatorInfo) bool { return v.ID == id }) {
		writeError(w, http.StatusBadRequest, "unknown_validator", "validatorId must be one of the ids listed by GET /api/validators")
		return
	}
	opts, ok := validateOptions(w, r)
	if !ok {
		return
	}
	// The limit leaves room for the multipart headers around the file.
	mr, err := multipartReader(r, http.MaxBytesReader(w, r.Body, s.cfg.MaxPackageSize+64<<10))
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be multipart/form-data")
		return
	}
	var part *multipart.Part
	for {
		part, err = mr.NextPart()
		if err != nil {
			if isTooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, validator.CodeTooLarge, "request is larger than the configured MAX_PACKAGE_SIZE")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_request", `multipart field "file" is missing`)
			return
		}
		if part.FormName() == "file" {
			break
		}
		part.Close()
	}
	ctx, release, ok := s.acquire(w, r)
	if !ok {
		return
	}
	defer release()

	f, size, ok := s.spool(w, part)
	if !ok {
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()

	var magic [4]byte
	n, _ := f.ReadAt(magic[:], 0)
	isZIP := n == 4 && string(magic[:]) == "PK\x03\x04"
	name := r.URL.Query().Get("name")
	if name == "" {
		name = filepath.Base(part.FileName())
	}
	if name == "" || name == "." {
		name = map[bool]string{true: "package.zip", false: "document.xml"}[isZIP]
	}
	meta := validator.ReportMeta{Generator: Name + " " + Version, InputName: name, Now: time.Now()}
	if isZIP {
		s.writePackageReport(ctx, w, f, size, opts, meta)
		return
	}
	res := s.validator.Validate(ctx, io.NewSectionReader(f, 0, size), opts)
	if res.Outcome == validator.OutcomeTooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, validator.CodeTooLarge, res.Errors[0].Message)
		return
	}
	writeJSON(w, statusFor(res.Outcome), validator.DocumentReport(res, meta))
}

// ValidatorInfo describes a validator in GET /api/validators, in the shape
// of the public OpenAPI at https://vc.1ed.tech/v3/api-docs (ValidatorInfo).
type ValidatorInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// Validators are the accepted values of validatorId on /api/validate. All
// select the same QTI 3 validation; the QTI version follows from the input or
// from the version parameter.
var Validators = []ValidatorInfo{
	{ID: "Qti30Inspector", Name: "QTI 3.0 Validator", Desc: "Validates QTI 3.0 packages and XML files"},
}

// apiValidators lists the accepted validatorId values.
func (s *Server) apiValidators(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Validators)
}

// multipartReader reads the multipart body of r from body.
func multipartReader(r *http.Request, body io.Reader) (*multipart.Reader, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, http.ErrNotMultipart
	}
	return multipart.NewReader(body, params["boundary"]), nil
}

// spool copies a package to a temporary file, because a ZIP must be read
// from the end. The caller removes the file. On failure the response has
// been written.
func (s *Server) spool(w http.ResponseWriter, body io.Reader) (*os.File, int64, bool) {
	f, err := os.CreateTemp("", "qti-package-*.zip")
	if err != nil {
		s.log.Error("create temp file", "error", err)
		writeError(w, http.StatusInternalServerError, validator.CodeInternal, "cannot store the package")
		return nil, 0, false
	}
	size, err := io.Copy(f, io.LimitReader(body, s.cfg.MaxPackageSize+1))
	if err == nil && size > s.cfg.MaxPackageSize {
		err = &http.MaxBytesError{Limit: s.cfg.MaxPackageSize}
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, validator.CodeTooLarge, "package is larger than the configured MAX_PACKAGE_SIZE")
			return nil, 0, false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "cannot read the request body")
		return nil, 0, false
	}
	return f, size, true
}

func isTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
}

func (s *Server) writePackageReport(ctx context.Context, w http.ResponseWriter, f *os.File, size int64, opts validator.ValidateOptions, meta validator.ReportMeta) {
	res := s.validator.ValidatePackage(ctx, f, size, validator.PackageLimits{
		MaxFiles:            s.cfg.MaxFiles,
		MaxFileSize:         s.cfg.MaxFileSize,
		MaxUncompressedSize: s.cfg.MaxUncompressedSize,
	}, opts)
	if res.Outcome == validator.OutcomeTooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, res.Errors[len(res.Errors)-1].Code, res.Errors[len(res.Errors)-1].Message)
		return
	}
	writeJSON(w, statusFor(res.Outcome), validator.PackageReport(res, meta))
}

// reportMeta describes the request for the report. The optional name query
// parameter names the input, for example the uploaded file's name.
func (s *Server) reportMeta(r *http.Request, defaultName string) validator.ReportMeta {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = defaultName
	}
	return validator.ReportMeta{Generator: Name + " " + Version, InputName: name, Now: time.Now()}
}

// validateOptions reads the optional version query parameter, which forces
// the QTI version every document is validated against.
func validateOptions(w http.ResponseWriter, r *http.Request) (validator.ValidateOptions, bool) {
	version := r.URL.Query().Get("version")
	if version != "" && !validator.IsSupportedVersion(version) {
		writeError(w, http.StatusBadRequest, validator.CodeUnsupportedVersion,
			"version must be one of "+strings.Join(validator.SupportedVersions(), ", "))
		return validator.ValidateOptions{}, false
	}
	return validator.ValidateOptions{Version: version}, true
}

// acquire waits for a validation slot, bounding concurrent work and with it
// peak memory. Requests that cannot get a slot before their timeout get 503.
func (s *Server) acquire(w http.ResponseWriter, r *http.Request) (context.Context, func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.RequestTimeout)
	select {
	case s.slots <- struct{}{}:
		return ctx, func() { <-s.slots; cancel() }, true
	case <-ctx.Done():
		cancel()
		writeError(w, http.StatusServiceUnavailable, "busy", "no validation slot available in time")
		return nil, nil, false
	}
}

// statusFor maps a validation outcome to the HTTP status of its report. A
// report is HTTP 200 whatever it found, as with 1EdTech's validators: the
// verdict is summary.outcome. Only a failure of the service itself is 500.
func statusFor(o validator.Outcome) int {
	if o == validator.OutcomeInternal {
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

// requestError is the body of a response without a report: the request
// could not be validated at all.
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

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// logRequests logs method, path, status and duration. Bodies are never logged.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic", "path", r.URL.Path, "panic", p)
				writeError(rec, http.StatusInternalServerError, validator.CodeInternal, "internal error")
			}
			s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
				"bytes_in", r.ContentLength, "duration_ms", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(rec, r)
	})
}
