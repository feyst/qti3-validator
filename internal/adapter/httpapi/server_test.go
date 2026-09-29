package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/bootstrap"
	"github.com/kennisnet/qti3-validator/internal/config"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
	"github.com/kennisnet/qti3-validator/internal/testutil"
)

var (
	once   sync.Once
	shared *app.Validator
)

// testOptions are the options a test server differs in.
type testOptions struct {
	MaxRequestSize int64 // limit of one document
	MaxPackageSize int64
}

func newTestServer(t *testing.T, o testOptions) *httptest.Server {
	t.Helper()
	once.Do(func() {
		v, err := bootstrap.NewValidator(bootstrap.Options{})
		if err != nil {
			panic(err)
		}
		shared = v
	})
	v := shared
	if o.MaxRequestSize != 0 {
		var err error
		v, err = bootstrap.NewValidator(bootstrap.Options{Limits: qti.Limits{MaxDocumentSize: o.MaxRequestSize}})
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.Default()
	opts := Options{
		MaxPackageSize: cfg.MaxPackageSize,
		MaxConcurrent:  cfg.MaxConcurrent,
		RequestTimeout: cfg.RequestTimeout,
	}
	if o.MaxPackageSize != 0 {
		opts.MaxPackageSize = o.MaxPackageSize
	}
	srv := httptest.NewServer(New(opts, v, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func testdata(t *testing.T, name string) []byte {
	t.Helper()
	return testutil.ReadTestdata(t, name)
}

func post(t *testing.T, url, contentType string, body []byte) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

// firstCode returns the code of a request error, or of the most severe
// item in a report.
func firstCode(body map[string]any) string {
	if code, ok := body["code"].(string); ok {
		return code
	}
	for _, list := range []string{"exceptions", "fatals", "errors", "warnings"} {
		if items, _ := body[list].([]any); len(items) > 0 {
			return items[0].(map[string]any)["code"].(string)
		}
	}
	return ""
}

// outcome returns summary.outcome of a report.
func outcome(body map[string]any) string {
	summary, _ := body["summary"].(map[string]any)
	o, _ := summary["outcome"].(string)
	return o
}

func TestValidateStatusCodes(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	tests := []struct {
		name        string
		contentType string
		body        []byte
		status      int
		outcome     string
		code        string
	}{
		{"valid", "application/xml", testdata(t, "valid/assessment-item.xml"), 200, "VALID", ""},
		{"text/xml", "text/xml; charset=utf-8", testdata(t, "valid/assessment-item.xml"), 200, "VALID", ""},
		{"invalid QTI", "application/xml", testdata(t, "invalid/invalid-value.xml"), 200, "ERROR", "validation"},
		{"unknown root", "application/xml", testdata(t, "invalid/unknown-root.xml"), 200, "FATAL", "unsupported_document"},
		{"malformed", "application/xml", testdata(t, "invalid/malformed.xml"), 200, "FATAL", "invalid_xml"},
		{"wrong content type", "application/json", []byte(`{}`), 415, "", "unsupported_media_type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := post(t, srv.URL+"/v1/validate", tt.contentType, tt.body)
			if status != tt.status || outcome(body) != tt.outcome || firstCode(body) != tt.code {
				t.Fatalf("got %d %q %q, want %d %q %q: %v", status, outcome(body), firstCode(body), tt.status, tt.outcome, tt.code, body)
			}
		})
	}
}

func TestReportShape(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	status, body := post(t, srv.URL+"/v1/validate?name=item.xml", "application/xml", testdata(t, "invalid/invalid-value.xml"))
	if status != 200 {
		t.Fatalf("status %d", status)
	}
	for _, key := range []string{
		"id", "generated", "generator", "input", "specification", "summary",
		"fatals", "errors", "warnings", "exceptions", "notRun", "valids",
	} {
		if _, ok := body[key]; !ok {
			t.Errorf("report has no %q", key)
		}
	}
	input := body["input"].(map[string]any)
	if input["name"] != "item.xml" || input["type"] != "XML" {
		t.Errorf("input %v", input)
	}
	item := body["errors"].([]any)[0].(map[string]any)
	loc := item["location"].(map[string]any)
	if loc["resource"] != "item.xml" || loc["line"] == nil || loc["column"] == nil || item["generator"] == "" {
		t.Errorf("item %v", item)
	}
	if v, ok := item["detailsMessage"]; !ok || v != nil {
		t.Errorf("detailsMessage should be present and null: %v", item)
	}
}

func TestValidateRequestTooLarge(t *testing.T) {
	srv := newTestServer(t, testOptions{MaxRequestSize: 512})
	status, body := post(t, srv.URL+"/v1/validate", "application/xml", testdata(t, "valid/assessment-item.xml"))
	if status != http.StatusRequestEntityTooLarge || firstCode(body) != "too_large" {
		t.Fatalf("got %d %v", status, body)
	}
}

func zipOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, _ := zw.Create(name)
		w.Write(data)
	}
	zw.Close()
	return buf.Bytes()
}

func TestValidatePackage(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	files := map[string][]byte{
		"imsmanifest.xml":  testdata(t, "package/imsmanifest.xml"),
		"test.xml":         testdata(t, "package/test.xml"),
		"items/item-1.xml": testdata(t, "package/item-1.xml"),
	}
	status, body := post(t, srv.URL+"/v1/validate/package", "application/zip", zipOf(t, files))
	if status != 200 || outcome(body) != "VALID" || body["summary"].(map[string]any)["valid"] != 3.0 {
		t.Fatalf("got %d %v", status, body)
	}

	files["items/item-2.xml"] = testdata(t, "invalid/malformed.xml")
	status, body = post(t, srv.URL+"/v1/validate/package", "application/zip", zipOf(t, files))
	if status != 200 || outcome(body) != "FATAL" || firstCode(body) != "invalid_xml" {
		t.Fatalf("got %d %v", status, body)
	}

	status, body = post(t, srv.URL+"/v1/validate/package", "application/zip", []byte("not a zip"))
	if status != 200 || outcome(body) != "FATAL" || firstCode(body) != "invalid_zip" {
		t.Fatalf("got %d %v", status, body)
	}

	status, _ = post(t, srv.URL+"/v1/validate/package", "application/xml", zipOf(t, files))
	if status != 415 {
		t.Fatalf("got %d", status)
	}
}

func TestVersionParameter(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	item := testdata(t, "valid/assessment-item-3.0.1.xml")
	for query, want := range map[string]string{"": "VALID", "?version=3.0.1": "VALID", "?version=3.0.0": "ERROR", "?version=3.1": "400"} {
		status, body := post(t, srv.URL+"/v1/validate"+query, "application/xml", item)
		got := outcome(body)
		if status != 200 {
			got = strconv.Itoa(status)
		}
		if got != want {
			t.Errorf("%q: got %s %v, want %s", query, got, body, want)
		}
		if want == "400" && firstCode(body) != "unsupported_version" {
			t.Errorf("%q: got %v", query, body)
		}
	}
	files := map[string][]byte{
		"imsmanifest.xml":  testdata(t, "package/imsmanifest.xml"),
		"items/item-1.xml": item,
	}
	status, body := post(t, srv.URL+"/v1/validate/package?version=3.0.1", "application/zip", zipOf(t, files))
	if status != 200 || body["specification"].(map[string]any)["version"] != "3.0.1" {
		t.Fatalf("package with version: %d %v", status, body)
	}
	status, _ = post(t, srv.URL+"/v1/validate/package?version=nope", "application/zip", zipOf(t, files))
	if status != 400 {
		t.Fatalf("package with bad version: %d", status)
	}
}

func TestValidatePackageTooLarge(t *testing.T) {
	srv := newTestServer(t, testOptions{MaxPackageSize: 1024})
	status, body := post(t, srv.URL+"/v1/validate/package", "application/zip", bytes.Repeat([]byte("x"), 4096))
	if status != http.StatusRequestEntityTooLarge || firstCode(body) != "too_large" {
		t.Fatalf("got %d %v", status, body)
	}
}

func TestHealthAndVersion(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	for path, key := range map[string]string{"/health": "status", "/version": "go_version"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || body[key] == nil {
			t.Fatalf("%s: %d %v", path, resp.StatusCode, body)
		}
	}
	resp, _ := http.Get(srv.URL + "/v1/validate")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /v1/validate: %d", resp.StatusCode)
	}
}

// postFile posts data in the multipart field "file".
func postFile(t *testing.T, url, field, filename string, data []byte) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("note", "ignored")
	w, _ := mw.CreateFormFile(field, filename)
	w.Write(data)
	mw.Close()
	return post(t, url, mw.FormDataContentType(), buf.Bytes())
}

func TestAPIValidate(t *testing.T) {
	srv := newTestServer(t, testOptions{})
	api := srv.URL + "/api/validate?validatorId=Qti30Inspector"
	files := map[string][]byte{
		"imsmanifest.xml":  testdata(t, "package/imsmanifest.xml"),
		"test.xml":         testdata(t, "package/test.xml"),
		"items/item-1.xml": testdata(t, "package/item-1.xml"),
	}
	status, body := postFile(t, api, "file", "toets.zip", zipOf(t, files))
	if status != 200 || outcome(body) != "VALID" || body["input"].(map[string]any)["name"] != "toets.zip" ||
		body["input"].(map[string]any)["type"] != "ZIP" {
		t.Fatalf("package: %d %v", status, body)
	}

	files["items/item-1.xml"] = testdata(t, "invalid/invalid-value.xml")
	status, body = postFile(t, api, "file", "toets.zip", zipOf(t, files))
	errs, _ := body["errors"].([]any)
	if status != 200 || outcome(body) != "ERROR" || len(errs) == 0 ||
		errs[0].(map[string]any)["location"].(map[string]any)["resource"] != "/items/item-1.xml" {
		t.Fatalf("invalid package: %d %v", status, body)
	}

	status, body = postFile(t, srv.URL+"/api/validate", "file", "item.xml", testdata(t, "valid/assessment-item.xml"))
	if status != 200 || outcome(body) != "VALID" || body["input"].(map[string]any)["type"] != "XML" {
		t.Fatalf("document without validatorId: %d %v", status, body)
	}

	status, body = postFile(t, api+"&version=3.0.0", "file", "item.xml", testdata(t, "valid/assessment-item-3.0.1.xml"))
	if status != 200 || outcome(body) != "ERROR" {
		t.Fatalf("version parameter: %d %v", status, body)
	}

	for _, tc := range []struct {
		url, field string
		want       int
		code       string
	}{
		{srv.URL + "/api/validate?validatorId=Nope", "file", 400, "unknown_validator"},
		{api, "upload", 400, "invalid_request"},
		{api + "&version=9", "file", 400, "unsupported_version"},
	} {
		status, body := postFile(t, tc.url, tc.field, "x.zip", zipOf(t, files))
		if status != tc.want || firstCode(body) != tc.code {
			t.Errorf("%s field %s: %d %v", tc.url, tc.field, status, body)
		}
	}

	resp, err := http.Get(srv.URL + "/api/validators")
	if err != nil {
		t.Fatal(err)
	}
	var list []ValidatorInfo
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(list) != 1 || list[0].ID != "Qti30Inspector" || list[0].Name == "" {
		t.Fatalf("validators: %d %v", resp.StatusCode, list)
	}

	if status, _ := post(t, api, "application/zip", zipOf(t, files)); status != 415 {
		t.Fatalf("raw body: %d", status)
	}

	small := newTestServer(t, testOptions{MaxPackageSize: 1024})
	status, body = postFile(t, small.URL+"/api/validate", "file", "big.zip", bytes.Repeat([]byte("x"), 4096))
	if status != 413 || firstCode(body) != "too_large" {
		t.Fatalf("too large: %d %v", status, body)
	}
}
