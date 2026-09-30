package httpapi

import (
	"io"
	"mime"
	"mime/multipart"
	"net/http"

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

// apiValidators serves GET /api/validators: the accepted validatorId values.
func (s *Server) apiValidators(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, Validators)
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
