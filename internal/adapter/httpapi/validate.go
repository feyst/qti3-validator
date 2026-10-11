package httpapi

import (
	"context"
	"io"
	"math"
	"net/http"
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
// input in the multipart field "file", answered with a report, and 400 for an
// unknown validatorId (the request shape follows the public OpenAPI at
// https://vc.1ed.tech/v3/api-docs). A report is HTTP 200 whatever it found,
// and 422 when validation stopped before the end (too_complex); see
// statusFor. The input may also be sent as the body: an XML document as
// application/xml, a package as application/zip.
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

	// Freed last, once the body is let go of and its temporary file, if it
	// has one, is removed.
	reservation, ok := s.reserveBody(w, r, shape)
	if !ok {
		return
	}
	defer reservation.release()

	// One deadline bounds the request: receiving the body, waiting for a
	// slot and validating. The response may be written writeGrace later.
	ctx, cancel := context.WithTimeout(r.Context(), s.opts.RequestTimeout)
	defer cancel()
	deadline, _ := ctx.Deadline()
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(deadline)
	_ = rc.SetWriteDeadline(deadline.Add(s.writeGrace))

	// length is the length of the input when the request declares it,
	// negative when it does not.
	body, uploadName, limit, length := io.Reader(r.Body), "", s.opts.MaxPackageSize, r.ContentLength
	switch shape {
	case shapeMultipart:
		part, ok := s.filePart(w, r)
		if !ok {
			return
		}
		body, uploadName = part, part.FileName()
		// The file is the request less the multipart headers around it:
		// how long it is shows only as it arrives, unless the request is
		// so long that it cannot be small.
		if length < smallRequestSize+multipartOverhead {
			length = -1
		}
	case shapeDocument:
		// The validator reports a document over its limit; one byte more is
		// enough for it to tell.
		limit = s.opts.MaxDocumentSize + 1
		body = io.LimitReader(body, limit)
	}

	// The input is received in full before a slot is taken, so a slow upload
	// holds none: slots bound the work of validation, not the network.
	u, ok := s.spool(w, body, limit, length, reservation)
	if !ok {
		return
	}
	defer u.close()

	large := u.size >= smallRequestSize
	slot := s.acquire(ctx, w, large)
	if slot == noSlot {
		return
	}
	// The validation reserves its memory from the hold as it goes, and may
	// give up its slot while it waits for room.
	hold := s.newMemoryHold(slot, large)
	defer hold.release()
	ctx = hold.context(ctx)

	isPackage := shape == shapePackage || shape != shapeDocument && isZIP(u)
	if isPackage {
		res := s.validator.ValidatePackage(ctx, app.ValidatePackage{
			Package: u, Size: u.size, Limits: s.opts.PackageLimits, Version: version,
		})
		if hold.refused(w) {
			return
		}
		s.extendWriteDeadline(rc)
		s.writePackageReport(w, res, newMeta(inputName(r, uploadName, defaultPackageName)))
		return
	}
	res := s.validator.ValidateDocument(ctx, app.ValidateDocument{Document: u.reader(), Size: u.size, Version: version})
	if hold.refused(w) {
		return
	}
	s.extendWriteDeadline(rc)
	s.writeDocumentReport(w, res, newMeta(inputName(r, uploadName, defaultDocumentName)))
}

// reserveBody returns the reservation in MaxSpoolSize for the body of r,
// empty until the body arrives. A Content-Length
// over the limit of the request shape, or over the whole budget, is refused
// as too large before the body is read. On failure the response has been
// written.
func (s *Server) reserveBody(w http.ResponseWriter, r *http.Request, shape requestShape) (*spoolReservation, bool) {
	// spool writes one byte past its limit to tell that a body is too large.
	most, limit := s.opts.MaxPackageSize+1, s.opts.MaxPackageSize
	tooLarge := "package is larger than the configured MAX_PACKAGE_SIZE"
	switch shape {
	case shapeDocument:
		// Cut one byte past the limit, for the validator to report; any
		// length is accepted.
		most, limit = s.opts.MaxDocumentSize+1, math.MaxInt64
	case shapeMultipart:
		limit, tooLarge = s.opts.MaxPackageSize+multipartOverhead, "request is larger than the configured MAX_PACKAGE_SIZE"
	case shapePackage, shapeSniff:
	}
	if r.ContentLength > limit {
		refuseUnread(w)
		writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), tooLarge)
		return nil, false
	}
	if r.ContentLength >= 0 {
		most = min(most, r.ContentLength)
		if most > s.opts.MaxSpoolSize {
			refuseUnread(w)
			writeSpoolTooLarge(w)
			return nil, false
		}
	}
	return s.newSpoolReservation(), true
}

// writeSpoolTooLarge answers 413 for a body that does not fit in
// MaxSpoolSize even with no other upload in progress: retrying cannot help.
func writeSpoolTooLarge(w http.ResponseWriter) {
	writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge),
		"request is larger than the configured MAX_SPOOL_SIZE, the room for all uploads at once")
}

// extendWriteDeadline gives the response writeGrace from now, however long
// validation overran REQUEST_TIMEOUT: the client gets its report rather than
// a reset connection.
func (s *Server) extendWriteDeadline(rc *http.ResponseController) {
	_ = rc.SetWriteDeadline(time.Now().Add(s.writeGrace))
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
		// The last error says why: the document is larger than a limit, or
		// needs more memory than the service allows a document.
		writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), res.Errors[len(res.Errors)-1].Message)
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

// isZIP reports whether f starts with a ZIP signature: a local file
// header, the end of central directory record of an empty archive, or the
// marker of a spanned archive.
func isZIP(f io.ReaderAt) bool {
	var magic [4]byte
	n, _ := f.ReadAt(magic[:], 0)
	switch string(magic[:n]) {
	case "PK\x03\x04", "PK\x05\x06", "PK\x07\x08":
		return true
	}
	return false
}
