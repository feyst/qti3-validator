package httpapi

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/app/report"
	"qti3-validator/internal/domain/qti"
)

// Default input names in the report, when the request names none.
const (
	defaultDocumentName = "document.xml"
	defaultPackageName  = "package.zip"
)

// multipartOverhead is the room the multipart headers around the file get
// beyond MaxPackageSize.
const multipartOverhead = 64 << 10

// The request shapes of POST /api/validate, chosen by Content-Type.
type requestShape int

const (
	shapeSniff     requestShape = iota // a body whose content says what it is
	shapeMultipart                     // the input in the multipart field "file"
	shapeDocument                      // one XML document as the body
	shapePackage                       // a ZIP as the body
)

// shapeOf picks the request shape. A Content-Type that names XML or a ZIP is
// followed; any other, or none, lets the content decide. That includes
// application/x-www-form-urlencoded, which curl sends for --data-binary
// without a -H.
func shapeOf(r *http.Request) requestShape {
	mt := mediaType(r)
	switch {
	case mt == "multipart/form-data":
		return shapeMultipart
	case mt == "application/xml" || mt == "text/xml" || strings.HasSuffix(mt, "+xml"):
		return shapeDocument
	case mt == "application/zip" || mt == "application/x-zip-compressed":
		return shapePackage
	}
	return shapeSniff
}

// apiValidate serves POST /api/validate, the one validation endpoint: the
// input in the multipart field "file", answered with HTTP 200 and a report,
// and 400 for an unknown validatorId (the request shape follows the public
// OpenAPI at https://vc.1ed.tech/v3/api-docs). The input may also be sent as
// the body: an XML document as application/xml, a package as
// application/zip.
func (s *Server) apiValidate(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("validatorId"); id != "" && !slices.ContainsFunc(Validators, func(v ValidatorInfo) bool { return v.ID == id }) {
		writeError(w, http.StatusBadRequest, codeUnknownValidator, "validatorId must be one of the ids listed by GET /api/validators")
		return
	}
	shape := shapeOf(r)
	version, ok := versionParam(w, r)
	if !ok {
		return
	}

	// One deadline bounds the request: receiving the body, waiting for a
	// slot and validating.
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.RequestTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	_ = http.NewResponseController(w).SetReadDeadline(deadline)

	body, uploadName, limit := io.Reader(r.Body), "", s.opts.MaxPackageSize
	switch shape {
	case shapeMultipart:
		part, ok := s.filePart(w, r)
		if !ok {
			return
		}
		body, uploadName = part, part.FileName()
	case shapeDocument:
		// The validator reports a document over its limit; one byte more is
		// enough for it to tell.
		limit = s.opts.MaxDocumentSize + 1
		body = io.LimitReader(body, limit)
	}

	// The input is received in full before a slot is taken, so a slow upload
	// holds none: slots bound the work of validation, not the network.
	f, size, ok := s.spool(w, body, limit)
	if !ok {
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()

	release, ok := s.acquire(ctx, w)
	if !ok {
		return
	}
	defer release()

	if shape == shapePackage || shape != shapeDocument && isZIPFile(f) {
		res := s.validator.ValidatePackage(ctx, app.ValidatePackage{
			Package: f, Size: size, Limits: s.opts.PackageLimits, Version: version,
		})
		s.writePackageReport(w, res, newMeta(inputName(r, uploadName, defaultPackageName)))
		return
	}
	res := s.validator.ValidateDocument(ctx, app.ValidateDocument{Document: io.NewSectionReader(f, 0, size), Version: version})
	s.writeDocumentReport(w, res, newMeta(inputName(r, uploadName, defaultDocumentName)))
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

// inputName names the input in the report: the name query parameter, else
// the uploaded file's name, else a default for its kind.
func inputName(r *http.Request, uploadName, defaultName string) string {
	if name := r.URL.Query().Get("name"); name != "" {
		return name
	}
	if name := filepath.Base(uploadName); uploadName != "" && name != "." {
		return name
	}
	return defaultName
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

// isZIPFile reports whether f starts with a ZIP signature: a local file
// header, the end of central directory record of an empty archive, or the
// marker of a spanned archive.
func isZIPFile(f *os.File) bool {
	var magic [4]byte
	n, _ := f.ReadAt(magic[:], 0)
	switch string(magic[:n]) {
	case "PK\x03\x04", "PK\x05\x06", "PK\x07\x08":
		return true
	}
	return false
}
