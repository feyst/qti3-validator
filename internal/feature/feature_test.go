// Package feature_test runs the service end to end: realistic packages and
// items go through the HTTP API, and each report is compared with a golden
// file in testdata/features/golden. After a deliberate change, update the
// golden files with
//
//	go test ./internal/feature -update
//
// and review the diff.
package feature_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/adapter/httpapi"
	"qti3-validator/internal/bootstrap"
	"qti3-validator/internal/config"
	"qti3-validator/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// A case is one request: a body, the Content-Type it is sent with, and the
// query string.
type featureCase struct {
	name        string
	body        func(t *testing.T) []byte
	contentType string
	query       string
}

// toets is the realistic package in testdata/features/packages/toets, with
// the given changes applied: a change maps a file to its new content, or to
// nil to leave the file out.
func toets(changes map[string]func(string) *string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		files := readDir(t, testutil.Testdata("features/packages/toets"))
		for name, change := range changes {
			if v := change(files[name]); v != nil {
				files[name] = *v
			} else {
				delete(files, name)
			}
		}
		return zipFiles(t, files)
	}
}

func replace(old, repl string) func(string) *string {
	return func(s string) *string {
		r := strings.Replace(s, old, repl, 1)
		return &r
	}
}

func with(content string) func(string) *string { return func(string) *string { return &content } }

func without() func(string) *string { return func(string) *string { return nil } }

func file(name string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte { return testutil.ReadTestdata(t, name) }
}

func example(name string) func(t *testing.T) []byte {
	return func(t *testing.T) []byte {
		t.Helper()
		data, err := os.ReadFile(testutil.Path("examples/" + name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
}

var cases = []featureCase{
	// A complete, valid package: twelve items of different interaction
	// types, a test with two sections, a stimulus and an image.
	{name: "toets", body: toets(nil), contentType: "application/zip"},
	{name: "toets-no-content-type", body: toets(nil)},
	{name: "toets-as-3.0.1", body: toets(nil), contentType: "application/zip", query: "version=3.0.1"},

	// One problem each.
	{name: "toets-malformed-xml", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/keuze.xml": func(s string) *string { r := s[:len(s)/2]; return &r },
	})},
	{name: "toets-schema-error", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/invul.xml": replace(`expected-length="10"`, `expected-length="tien"`),
	})},
	{name: "toets-1edtech-rule", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/meerkeuze.xml": replace(`<qti-choice-interaction `, `<qti-choice-interaction colour="red" `),
	})},
	{name: "toets-additional-check", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/volgorde.xml": replace(`<qti-order-interaction response-identifier="RESPONSE"`, `<qti-order-interaction response-identifier="ANTWOORD"`),
	})},
	{name: "toets-qti2-item", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/keuzelijst.xml": replace(`xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"`, `xmlns="http://www.imsglobal.org/xsd/imsqti_v2p2"`),
	})},
	{name: "toets-unsupported-version", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"imsmanifest.xml": replace(`<schemaversion>3.0.0</schemaversion>`, `<schemaversion>2.2</schemaversion>`),
	})},
	{name: "toets-missing-manifest", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"imsmanifest.xml": without(),
	})},
	{name: "toets-unsafe-path", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"../buiten.xml": with(`<x/>`),
	})},
	{name: "toets-missing-image", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"media/kaart.png": without(),
	})},
	{name: "toets-wrong-item-ref", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"test.xml": replace(`href="items/koppel.xml"`, `href="items/koppelen.xml"`),
	})},
	{name: "toets-unlisted-file", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"media/extra.png": with("not really a PNG"),
	})},
	{name: "toets-type-error", contentType: "application/zip", body: toets(map[string]func(string) *string{
		"items/keuze.xml": replace(`<qti-base-value base-type="identifier">GOED</qti-base-value>`, `<qti-base-value base-type="float">1</qti-base-value>`),
	})},
	{name: "not-a-zip", contentType: "application/zip", body: func(*testing.T) []byte { return []byte("dit is geen zip") }},

	// Single documents.
	{name: "item", body: file("features/packages/toets/items/keuze.xml"), contentType: "application/xml", query: "name=keuze.xml"},
	{name: "item-mathml-as-3.0.0", body: file("features/packages/toets/items/formule.xml"), query: "version=3.0.0&name=formule.xml"},

	// The example packages in examples/.
	{name: "example-valid", body: example("valid.zip")},
	{name: "example-with-errors", body: example("with-errors.zip")},

	// Packages from php-qti3 (MIT, see testdata/features/php-qti3/LICENSE).
	{name: "php-qti3-valid-package", body: file("features/php-qti3/valid-package.zip"), contentType: "application/zip"},
	{name: "php-qti3-invalid-response-processing", body: file("features/php-qti3/invalid-response-processing.zip"), contentType: "application/zip"},
	{name: "php-qti3-no-response-processing", body: file("features/php-qti3/no-response-processing.zip"), contentType: "application/zip"},
	{name: "php-qti3-no-max-score-all-items", body: file("features/php-qti3/no-max-score-all-items.zip"), contentType: "application/zip"},
}

func TestFeatures(t *testing.T) {
	v, err := bootstrap.NewValidator(bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		MaxPackageSize: cfg.MaxPackageSize, MaxConcurrent: cfg.MaxConcurrent, RequestTimeout: cfg.RequestTimeout,
	}, v, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(srv.Close)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := post(t, srv.URL+"/api/validate?"+tc.query, tc.contentType, tc.body(t))
			golden := testutil.Testdata("features/golden/" + tc.name + ".json")
			if *update {
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v; run go test ./internal/feature -update", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("report differs from %s; run go test ./internal/feature -update and review the diff:\n%s",
					filepath.Base(golden), firstDifference(want, got))
			}
		})
	}
}

// post sends a request and returns the response, status included, as
// stable, indented JSON: the report's id and time, which differ per run, are
// left out.
func post(t *testing.T, url, contentType string, body []byte) []byte {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var report map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	delete(report, "id")
	delete(report, "generated")
	report["generator"] = httpapi.Name // without the version
	out, err := json.MarshalIndent(map[string]any{"status": resp.StatusCode, "report": report}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(out, '\n')
}

func readDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// zipFiles packs files in name order with a fixed time, so the same files
// give the same ZIP.
func zipFiles(t *testing.T, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, name := range names {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// firstDifference shows the first differing line of two JSON documents.
func firstDifference(want, got []byte) string {
	w, g := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := range max(len(w), len(g)) {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return "line " + strconv.Itoa(i+1) + ":\n  want " + wl + "\n  got  " + gl
		}
	}
	return ""
}

// TestCorpus validates every QTI 3 file of the public 1EdTech examples that
// `make corpus` fetches, and compares the outcome and finding codes per file
// with testdata/features/golden/corpus.json. Without the corpus it is
// skipped.
func TestCorpus(t *testing.T) {
	root := testutil.Testdata("corpus/qti-examples")
	if _, err := os.Stat(root); err != nil {
		t.Skip("no corpus; run make corpus")
	}
	v, err := bootstrap.NewValidator(bootstrap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	srv := httptest.NewServer(httpapi.New(httpapi.Options{
		MaxPackageSize: cfg.MaxPackageSize, MaxConcurrent: cfg.MaxConcurrent, RequestTimeout: cfg.RequestTimeout,
	}, v, slog.New(slog.DiscardHandler)).Handler())
	t.Cleanup(srv.Close)

	type summary struct {
		Outcome string   `json:"outcome"`
		Codes   []string `json:"codes,omitempty"`
	}
	got := map[string]summary{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasPrefix(rel, "qtiv3-examples/") && !strings.HasPrefix(rel, "QTI3_") {
			return nil
		}
		if ext := strings.ToLower(filepath.Ext(rel)); ext != ".xml" && ext != ".zip" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var res struct {
			Status int `json:"status"`
			Report struct {
				Summary struct {
					Outcome string `json:"outcome"`
				} `json:"summary"`
				Fatals, Errors, Warnings []struct {
					Code string `json:"code"`
				}
			} `json:"report"`
		}
		if err := json.Unmarshal(post(t, srv.URL+"/api/validate", "", data), &res); err != nil {
			return err
		}
		if res.Status != http.StatusOK || res.Report.Summary.Outcome == "EXCEPTION" {
			t.Errorf("%s: status %d, outcome %s", rel, res.Status, res.Report.Summary.Outcome)
		}
		s := summary{Outcome: res.Report.Summary.Outcome}
		seen := map[string]bool{}
		for _, list := range [][]struct {
			Code string `json:"code"`
		}{res.Report.Fatals, res.Report.Errors, res.Report.Warnings} {
			for _, it := range list {
				if !seen[it.Code] {
					seen[it.Code] = true
					s.Codes = append(s.Codes, it.Code)
				}
			}
		}
		sort.Strings(s.Codes)
		got[rel] = s
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, '\n')
	golden := testutil.Testdata("features/golden/corpus.json")
	if *update {
		if err := os.WriteFile(golden, out, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v; run go test ./internal/feature -run Corpus -update", err)
	}
	if !bytes.Equal(out, want) {
		t.Errorf("corpus results differ from corpus.json; run go test ./internal/feature -run Corpus -update and review the diff:\n%s",
			firstDifference(want, out))
	}
}
