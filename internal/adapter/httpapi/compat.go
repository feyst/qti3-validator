package httpapi

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

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

// multipartOverhead is the room the multipart headers around the file get
// beyond MaxPackageSize.
const multipartOverhead = 64 << 10

// apiValidators serves GET /api/validators: the accepted validatorId values.
func (s *Server) apiValidators(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Validators)
}

// apiValidate serves POST /api/validate?validatorId=… with the input in the
// multipart field "file", answered with HTTP 200 and a report, and 400 for
// an unknown validatorId. The request shape follows the public OpenAPI at
// https://vc.1ed.tech/v3/api-docs. The file may be a package (ZIP) or a
// single XML document.
func (s *Server) apiValidate(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("validatorId"); id != "" && !slices.ContainsFunc(Validators, func(v ValidatorInfo) bool { return v.ID == id }) {
		writeError(w, http.StatusBadRequest, codeUnknownValidator, "validatorId must be one of the ids listed by GET /api/validators")
		return
	}
	version, ok := versionParam(w, r)
	if !ok {
		return
	}
	part, ok := s.filePart(w, r)
	if !ok {
		return
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

	isZIP := isZIPFile(f)
	meta := newMeta(uploadName(r, part, isZIP))
	if isZIP {
		res := s.validator.ValidatePackage(ctx, app.ValidatePackage{
			Package: f, Size: size, Limits: s.opts.PackageLimits, Version: version,
		})
		s.writePackageReport(w, res, meta)
		return
	}
	res := s.validator.ValidateDocument(ctx, app.ValidateDocument{Document: io.NewSectionReader(f, 0, size), Version: version})
	s.writeDocumentReport(w, res, meta)
}

// filePart returns the multipart field "file". On failure the response has
// been written.
func (s *Server) filePart(w http.ResponseWriter, r *http.Request) (*multipart.Part, bool) {
	mr, err := multipartReader(r, http.MaxBytesReader(w, r.Body, s.opts.MaxPackageSize+multipartOverhead))
	if err != nil {
		writeError(w, http.StatusUnsupportedMediaType, codeUnsupportedMediaType, "Content-Type must be multipart/form-data")
		return nil, false
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			if isTooLarge(err) {
				writeError(w, http.StatusRequestEntityTooLarge, string(qti.CodeTooLarge), "request is larger than the configured MAX_PACKAGE_SIZE")
				return nil, false
			}
			writeError(w, http.StatusBadRequest, codeInvalidRequest, `multipart field "file" is missing`)
			return nil, false
		}
		if part.FormName() == "file" {
			return part, true
		}
		_ = part.Close()
	}
}

// multipartReader reads the multipart body of r from body.
func multipartReader(r *http.Request, body io.Reader) (*multipart.Reader, error) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		return nil, http.ErrNotMultipart
	}
	return multipart.NewReader(body, params["boundary"]), nil
}

// isZIPFile reports whether f starts with a ZIP local file header.
func isZIPFile(f *os.File) bool {
	var magic [4]byte
	n, _ := f.ReadAt(magic[:], 0)
	return n == len(magic) && string(magic[:]) == "PK\x03\x04"
}

// uploadName names the input in the report: the name query parameter, else
// the uploaded file's name, else a default for its kind.
func uploadName(r *http.Request, part *multipart.Part, isZIP bool) string {
	if name := r.URL.Query().Get("name"); name != "" {
		return name
	}
	if name := filepath.Base(part.FileName()); name != "" && name != "." {
		return name
	}
	if isZIP {
		return defaultPackageName
	}
	return defaultDocumentName
}
