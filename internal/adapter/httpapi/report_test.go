package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// scriptedValidator answers every document with doc and every package with
// pkg, and keeps what it was asked.
type scriptedValidator struct {
	doc qti.DocumentResult
	pkg qti.PackageResult

	documents, packages int
	gotPackage          app.ValidatePackage
	gotBytes            []byte // the package or document as the validator read it
}

func (v *scriptedValidator) ValidateDocument(_ context.Context, q app.ValidateDocument) qti.DocumentResult {
	v.documents++
	v.gotBytes, _ = io.ReadAll(q.Document)
	return v.doc
}

func (v *scriptedValidator) ValidatePackage(_ context.Context, q app.ValidatePackage) qti.PackageResult {
	v.packages++
	v.gotPackage = q
	v.gotBytes = make([]byte, q.Size)
	_, _ = q.Package.ReadAt(v.gotBytes, 0)
	return v.pkg
}

// list returns one of the lists of a report.
func list(t *testing.T, body map[string]any, name string) []map[string]any {
	t.Helper()
	raw, ok := body[name].([]any)
	if !ok {
		t.Fatalf("no list %q in %v", name, body)
	}
	items := make([]map[string]any, len(raw))
	for i, it := range raw {
		items[i] = it.(map[string]any)
	}
	return items
}

// A failure of the validator itself is HTTP 500, still with a report: the
// findings so far and an EXCEPTION with code internal.
func TestInternalErrorIsReported(t *testing.T) {
	internal := qti.Finding{Code: qti.CodeInternal, Message: "schema engine failed"}
	found := qti.Finding{Code: qti.CodeValidation, Line: 3, Column: 5, Message: "unexpected element"}
	v := &scriptedValidator{
		doc: qti.DocumentResult{
			Schema: "qti-assessment-item", Version: "3.0.0", Outcome: qti.OutcomeInternal,
			Errors: []qti.Finding{found, internal},
		},
		pkg: qti.PackageResult{
			Version: "3.0.0", Outcome: qti.OutcomeInternal,
			Errors: []qti.Finding{internal},
			Files: []qti.FileResult{
				{File: "imsmanifest.xml", Valid: true, Schema: "imscp-manifest", Version: "3.0.0"},
				{File: "items/a.xml", Schema: "qti-assessment-item", Version: "3.0.0", Errors: []qti.Finding{found}},
			},
		},
	}
	srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20}, v)

	for name, tc := range map[string]struct {
		contentType, inputType string
		body                   []byte
	}{
		"document": {"application/xml", "XML", []byte("<a/>")},
		"package":  {"application/zip", "ZIP", zipOf(t, map[string][]byte{"imsmanifest.xml": []byte("<manifest/>")})},
		"sniffed":  {"", "ZIP", zipOf(t, nil)},
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := http.Post(srv.URL+"/api/validate", tc.contentType, bytes.NewReader(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			r := decode(resp)
			if r.status != http.StatusInternalServerError {
				t.Fatalf("status %d, want 500: %v", r.status, r.body)
			}
			if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type %q", ct)
			}
			body := r.body
			if outcome(body) != "EXCEPTION" || firstCode(body) != "internal" {
				t.Fatalf("outcome %s, first code %s: %v", outcome(body), firstCode(body), body)
			}
			if input := body["input"].(map[string]any); input["type"] != tc.inputType {
				t.Errorf("input %v, want type %s", input, tc.inputType)
			}
			exceptions := list(t, body, "exceptions")
			if len(exceptions) != 1 || exceptions[0]["message"] != "schema engine failed" {
				t.Errorf("exceptions %v", exceptions)
			}
			// What was found before the failure is still reported.
			if errs := list(t, body, "errors"); len(errs) != 1 || errs[0]["code"] != "validation" {
				t.Errorf("errors %v", errs)
			}
			if _, isRequestError := body["code"]; isRequestError {
				t.Errorf("a request error instead of a report: %v", body)
			}
		})
	}
}

// Every way a package result reaches the client: as a report with the
// status of its outcome, or as a request error when the package is refused
// for its size.
func TestPackageReportPaths(t *testing.T) {
	manifest := qti.FileResult{File: "imsmanifest.xml", Valid: true, Schema: "imscp-manifest", Version: "3.0.1", Ran: qti.Checks{Schema: true}}
	tests := []struct {
		name   string
		result qti.PackageResult

		status  int
		outcome string // of a report; empty for a request error
		code    string
		check   func(t *testing.T, body map[string]any)
	}{
		{
			name: "valid",
			result: qti.PackageResult{
				Valid: true, Version: "3.0.1", Outcome: qti.OutcomeValid,
				Files: []qti.FileResult{
					manifest,
					{File: "items/a.xml", Valid: true, Schema: "qti-assessment-item", Version: "3.0.1"},
					{File: "extra/config.xml", Valid: true, Skipped: true},
				},
			},
			status: http.StatusOK, outcome: "VALID",
			check: func(t *testing.T, body map[string]any) {
				valids := list(t, body, "valids")
				if len(valids) != 2 || valids[0]["location"].(map[string]any)["resource"] != "/imsmanifest.xml" {
					t.Errorf("valids %v", valids)
				}
				if notRun := list(t, body, "notRun"); len(notRun) != 1 || notRun[0]["code"] != "skipped" {
					t.Errorf("notRun %v", notRun)
				}
				if spec := body["specification"].(map[string]any); spec["version"] != "3.0.1" {
					t.Errorf("specification %v", spec)
				}
			},
		},
		{
			name: "errors",
			result: qti.PackageResult{
				Version: "3.0.1", Outcome: qti.OutcomeInvalid,
				Errors:   []qti.Finding{{Code: qti.CodeReference, File: "imsmanifest.xml", Message: "items/b.xml is not in the package"}},
				Warnings: []qti.Finding{{Code: qti.CodeNotChecked, File: "items/frag.xml", Message: "not checked"}},
				Files: []qti.FileResult{
					manifest,
					{
						File: "items/a.xml", Schema: "qti-assessment-item", Version: "3.0.1",
						Errors: []qti.Finding{{Code: qti.CodeSchematron, Rule: "p", Line: 7, Message: "rule broken"}},
					},
				},
			},
			status: http.StatusOK, outcome: "ERROR", code: "reference",
			check: func(t *testing.T, body map[string]any) {
				errs := list(t, body, "errors")
				if len(errs) != 2 {
					t.Fatalf("errors %v", errs)
				}
				if loc := errs[0]["location"].(map[string]any); loc["resource"] != "/imsmanifest.xml" {
					t.Errorf("package finding at %v", loc)
				}
				if loc := errs[1]["location"].(map[string]any); loc["resource"] != "/items/a.xml" || loc["line"] != 7.0 {
					t.Errorf("document finding at %v", loc)
				}
				if warnings := list(t, body, "warnings"); len(warnings) != 1 || warnings[0]["code"] != "not_checked" {
					t.Errorf("warnings %v", warnings)
				}
			},
		},
		{
			name: "invalid_zip",
			result: qti.PackageResult{
				Outcome: qti.OutcomeMalformed,
				Errors:  []qti.Finding{{Code: qti.CodeInvalidZIP, Message: "not a ZIP archive"}},
			},
			status: http.StatusOK, outcome: "FATAL", code: "invalid_zip",
			check: func(t *testing.T, body map[string]any) {
				fatals := list(t, body, "fatals")
				if len(fatals) != 1 || fatals[0]["location"].(map[string]any)["resource"] != "upload.zip" {
					t.Errorf("fatals %v", fatals)
				}
			},
		},
		{
			name: "missing_manifest",
			result: qti.PackageResult{
				Outcome: qti.OutcomeInvalid,
				Errors:  []qti.Finding{{Code: qti.CodeMissingManifest, Message: "no imsmanifest.xml"}},
			},
			status: http.StatusOK, outcome: "ERROR", code: "missing_manifest",
		},
		{
			// Refused for its size: a request error, not a report. The last
			// finding names the limit, whatever was found before it.
			name: "too_many_files",
			result: qti.PackageResult{
				Outcome: qti.OutcomeTooLarge,
				Errors: []qti.Finding{
					{Code: qti.CodeUnsafePath, File: "../x.xml", Message: "unsafe path"},
					{Code: qti.CodeTooManyFiles, Message: "package has more than 2000 files"},
				},
			},
			status: http.StatusRequestEntityTooLarge, code: "too_many_files",
			check: func(t *testing.T, body map[string]any) {
				if body["message"] != "package has more than 2000 files" || len(body) != 2 {
					t.Errorf("got %v", body)
				}
			},
		},
		{
			name: "too_large",
			result: qti.PackageResult{
				Outcome: qti.OutcomeTooLarge,
				Errors:  []qti.Finding{{Code: qti.CodeTooLarge, Message: "package unpacks to more than the limit"}},
			},
			status: http.StatusRequestEntityTooLarge, code: "too_large",
		},
		{
			name: "too_complex",
			result: qti.PackageResult{
				Version: "3.0.1", Outcome: qti.OutcomeTooComplex,
				Errors: []qti.Finding{{Code: qti.CodeTooComplex, Message: "out of time"}},
				Files:  []qti.FileResult{manifest},
			},
			status: http.StatusUnprocessableEntity, outcome: "EXCEPTION", code: "too_complex",
		},
		{
			name: "internal",
			result: qti.PackageResult{
				Outcome: qti.OutcomeInternal,
				Errors:  []qti.Finding{{Code: qti.CodeInternal, Message: "cannot read the package"}},
			},
			status: http.StatusInternalServerError, outcome: "EXCEPTION", code: "internal",
		},
	}
	archive := zipOf(t, map[string][]byte{"imsmanifest.xml": []byte("<manifest/>")})
	limits := qti.PackageLimits{MaxFiles: 7}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := &scriptedValidator{pkg: tc.result}
			srv, _ := newFakeServer(t, Options{MaxPackageSize: 1 << 20, PackageLimits: limits}, v)
			status, body := post(t, srv.URL+"/api/validate?name=upload.zip&version=3.0.1", "application/zip", archive)
			if status != tc.status || outcome(body) != tc.outcome || firstCode(body) != tc.code {
				t.Fatalf("got %d, outcome %q, code %q, want %d, %q, %q: %v",
					status, outcome(body), firstCode(body), tc.status, tc.outcome, tc.code, body)
			}
			if tc.outcome != "" {
				if input := body["input"].(map[string]any); input["type"] != "ZIP" || input["name"] != "upload.zip" {
					t.Errorf("input %v", input)
				}
			} else if _, isReport := body["summary"]; isReport {
				t.Errorf("a report for a refused package: %v", body)
			}
			if tc.check != nil {
				tc.check(t, body)
			}
			// The package reached the validator whole, with the server's
			// limits and the requested version.
			if v.packages != 1 || v.documents != 0 {
				t.Fatalf("%d packages, %d documents validated", v.packages, v.documents)
			}
			if !bytes.Equal(v.gotBytes, archive) || v.gotPackage.Limits != limits || v.gotPackage.Version != "3.0.1" {
				t.Errorf("validator got %d bytes, limits %+v, version %q", len(v.gotBytes), v.gotPackage.Limits, v.gotPackage.Version)
			}
		})
	}
}
