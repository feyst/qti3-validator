package httpapi

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/app/report"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// Default input names in the report, when the request names none.
const (
	defaultDocumentName = "document.xml"
	defaultPackageName  = "package.zip"
)

// validateDocument serves POST /v1/validate: one XML document as the body.
func (s *Server) validateDocument(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r, isXMLMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/xml")
		return
	}
	version, ok := versionParam(w, r)
	if !ok {
		return
	}
	ctx, release, ok := s.acquire(w, r)
	if !ok {
		return
	}
	defer release()

	res := s.validator.ValidateDocument(ctx, app.ValidateDocument{Document: r.Body, Version: version})
	s.writeDocumentReport(w, res, s.reportMeta(r, defaultDocumentName))
}

// validatePackage serves POST /v1/validate/package: a ZIP as the body.
func (s *Server) validatePackage(w http.ResponseWriter, r *http.Request) {
	if !hasMediaType(r, isZIPMediaType) {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be application/zip")
		return
	}
	version, ok := versionParam(w, r)
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
	res := s.validator.ValidatePackage(ctx, app.ValidatePackage{
		Package: f, Size: size, Limits: s.opts.PackageLimits, Version: version,
	})
	s.writePackageReport(w, res, s.reportMeta(r, defaultPackageName))
}

// versionParam reads the optional version query parameter, which forces the
// QTI version every document is validated against.
func versionParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	version := r.URL.Query().Get("version")
	if version != "" && !qti.IsSupportedVersion(version) {
		writeError(w, http.StatusBadRequest, string(qti.CodeUnsupportedVersion),
			"version must be one of "+strings.Join(qti.SupportedVersions(), ", "))
		return "", false
	}
	return version, true
}

// reportMeta describes the request for the report. The optional name query
// parameter names the input, for example the uploaded file's name.
func (s *Server) reportMeta(r *http.Request, defaultName string) report.Meta {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = defaultName
	}
	return newMeta(name)
}

func newMeta(inputName string) report.Meta {
	return report.Meta{Generator: Name + " " + Version, InputName: inputName, Now: time.Now()}
}

func (s *Server) writeDocumentReport(w http.ResponseWriter, res qti.DocumentResult, meta report.Meta) {
	if res.Outcome == qti.OutcomeTooLarge {
		writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), res.Errors[0].Message)
		return
	}
	writeJSON(w, statusFor(res.Outcome), report.ForDocument(res, meta))
}

func (s *Server) writePackageReport(w http.ResponseWriter, res qti.PackageResult, meta report.Meta) {
	if res.Outcome == qti.OutcomeTooLarge {
		last := res.Errors[len(res.Errors)-1]
		writeError(w, http.StatusRequestEntityTooLarge, string(last.Code), last.Message)
		return
	}
	writeJSON(w, statusFor(res.Outcome), report.ForPackage(res, meta))
}
