package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	schemaPath = "/spec/x/schema.xsd"
	schema     = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:element name="a"/></xs:schema>`
	rules      = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
<sch:pattern id="p"><sch:rule context="a"><sch:assert test="@b">b is required</sch:assert></sch:rule></sch:pattern>
</sch:schema>`
)

func sum(data string) string {
	s := sha256.Sum256([]byte(data))
	return hex.EncodeToString(s[:])
}

// upstream is an https server that serves files by path and counts its
// requests.
type upstream struct {
	*httptest.Server
	requests atomic.Int64
}

func newUpstream(t *testing.T, files map[string]string, extra ...func(mux *http.ServeMux)) *upstream {
	t.Helper()
	u := &upstream{}
	mux := http.NewServeMux()
	for name, body := range files {
		mux.HandleFunc(name, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
	}
	for _, e := range extra {
		e(mux)
	}
	u.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

// fetcher returns a fetcher that trusts the test server's certificate and
// has the redirect policy of the command.
func (u *upstream) fetcher(outDir string) fetcher {
	return fetcher{client: newClient(u.Client()), outDir: outDir, maxBytes: maxSchemaBytes}
}

func (u *upstream) pin(t *testing.T, urlPath, sha string) pin {
	t.Helper()
	parsed, err := url.Parse(u.URL + urlPath)
	if err != nil {
		t.Fatal(err)
	}
	return pin{sha256: sha, url: parsed}
}

// stored is where fetch keeps the schema of p in outDir.
func stored(t *testing.T, outDir string, p pin) string {
	t.Helper()
	name, err := localName(p)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(outDir, name)
}

func writeGzip(t *testing.T, name, content string) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = io.WriteString(zw, content)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeFile(t, name, buf.String())
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readGzip(t *testing.T, name string) string {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// files lists the regular files and links under dir, relative to it.
func files(t *testing.T, dir string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, name)
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func wantError(t *testing.T, err error, text string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), text) {
		t.Fatalf("got error %v, want one containing %q", err, text)
	}
}

func TestReadLock(t *testing.T) {
	sha := sum("x")
	lock := filepath.Join(t.TempDir(), "schemas.lock")
	writeFile(t, lock, "# sha256 url\n\n"+sha+"  https://example.org/a/b.xsd\n\t"+sha+" https://example.org/c.xsd \n")
	pins, err := readLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 2 || pins[0].sha256 != sha || pins[0].url.String() != "https://example.org/a/b.xsd" || pins[1].url.Path != "/c.xsd" {
		t.Fatalf("got %+v", pins)
	}

	for name, tc := range map[string]struct{ content, want string }{
		"short checksum":   {sha[:63] + " https://example.org/a.xsd\n", `:1: want "<sha256> <url>"`},
		"long checksum":    {sha + "0 https://example.org/a.xsd\n", `:1: want "<sha256> <url>"`},
		"no URL":           {"# c\n" + sha + "\n", `:2: want "<sha256> <url>"`},
		"three fields":     {sha + " https://example.org/a.xsd extra\n", `:1: want "<sha256> <url>"`},
		"checksum not hex": {sha[:63] + "g https://example.org/a.xsd\n", ":1: want \"<sha256> <url>\": the checksum is not 64 hexadecimal digits"},
		"host of a dot":    {sha + " https://./a.xsd\n", ":1: want an https URL with a host name"},
		"host of dots":     {sha + " https://../a.xsd\n", ":1: want an https URL with a host name"},
		"host without dot": {sha + " https://schemas/a.xsd\n", ":1: want an https URL with a host name"},
		"only a port":      {sha + " https://:443/a.xsd\n", ":1: want an https URL with a host name"},
		"http":             {sha + " http://example.org/a.xsd\n", ":1: want an https URL"},
		"file":             {sha + " file:///etc/passwd\n", ":1: want an https URL"},
		"no scheme":        {sha + " example.org/a.xsd\n", ":1: want an https URL"},
		"no host":          {sha + " https:///a.xsd\n", ":1: want an https URL"},
		"malformed URL":    {sha + " https://exa mple.org/%zz\n", "want"},
		"nothing listed":   {"# only a comment\n\n", "no schemas listed"},
		"second line http": {sha + " https://example.org/a.xsd\n" + sha + " http://example.org/b.xsd\n", ":2: want an https URL"},
	} {
		t.Run(name, func(t *testing.T) {
			writeFile(t, lock, tc.content)
			pins, err := readLock(lock)
			wantError(t, err, tc.want)
			if pins != nil {
				t.Fatalf("got pins %+v with an error", pins)
			}
		})
	}

	if _, err := readLock(filepath.Join(t.TempDir(), "missing.lock")); err == nil {
		t.Fatal("a missing lock file is accepted")
	}

	// A checksum in upper case is the same checksum: it is compared in
	// lower case, as sha256sum and this command write it.
	writeFile(t, lock, strings.ToUpper(sha)+" https://example.org/a.xsd\n")
	pins, err = readLock(lock)
	if err != nil || len(pins) != 1 || pins[0].sha256 != sha {
		t.Fatalf("upper case checksum: got %+v, %v", pins, err)
	}
}

// A schema is stored under its host, so a host that is no name of a
// directory of its own is refused, also for a pin that did not come from a
// lock file.
func TestLocalNameNeedsHostName(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://purl.example.org/a.xsd": true,
		"https://127.0.0.1:8443/a.xsd":   true,
		"https://./a.xsd":                false,
		"https://../a.xsd":               false,
		"https://.../a.xsd":              false,
		"https://schemas/a.xsd":          false,
		"https://localhost:8443/a.xsd":   false,
		"https:///a.xsd":                 false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		name, err := localName(pin{url: u})
		if ok != (err == nil) {
			t.Errorf("%s: got %q, %v", raw, name, err)
		}
	}
}

// The redirect policy itself, without a server: https only, the pinned host
// only, and at most maxRedirects redirects.
func TestCheckRedirect(t *testing.T) {
	request := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	// via holds the requests made so far: the first one and n-1 redirects.
	via := func(n int) []*http.Request {
		reqs := make([]*http.Request, n)
		for i := range reqs {
			reqs[i] = request("https://purl.example.org/a.xsd")
		}
		return reqs
	}
	if maxRedirects != 5 {
		t.Fatalf("maxRedirects is %d; this test is written for 5", maxRedirects)
	}
	for _, tc := range []struct {
		name, to string
		via      int
		want     string // part of the error; empty when followed
	}{
		{"https on the same host", "https://purl.example.org/b.xsd", 1, ""},
		{"http on the same host", "http://purl.example.org/b.xsd", 1, "refused: only https on purl.example.org"},
		{"https on another host", "https://other.example.org/b.xsd", 1, "refused: only https on purl.example.org"},
		{"https on another port", "https://purl.example.org:8443/b.xsd", 1, "refused: only https on purl.example.org"},
		{"https on a subdomain", "https://www.purl.example.org/b.xsd", 1, "refused: only https on purl.example.org"},
		{"http on another host", "http://other.example.org/b.xsd", 1, "refused: only https on purl.example.org"},
		{"another scheme", "ftp://purl.example.org/b.xsd", 1, "refused: only https on purl.example.org"},
		{"fifth redirect", "https://purl.example.org/b.xsd", 5, ""},
		{"sixth redirect", "https://purl.example.org/b.xsd", 6, "more than 5 redirects"},
		{"many redirects", "https://purl.example.org/b.xsd", 50, "more than 5 redirects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRedirect(request(tc.to), via(tc.via))
			if tc.want == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			wantError(t, err, tc.want)
		})
	}
}

// The output directory is written through a root: a host directory that is
// a link to somewhere else is not written through, read from or removed
// from.
func TestFetchDoesNotFollowLinksOutOfOutput(t *testing.T) {
	up := newUpstream(t, map[string]string{schemaPath: schema})
	host := strings.TrimPrefix(up.URL, "https://")
	p := up.pin(t, schemaPath, sum(schema))

	t.Run("host directory", func(t *testing.T) {
		out, outside := t.TempDir(), t.TempDir()
		if err := os.Symlink(outside, filepath.Join(out, host)); err != nil {
			t.Fatal(err)
		}
		if _, err := up.fetcher(out).fetch(p); err == nil {
			t.Error("fetched through a link out of the output directory")
		}
		if got := files(t, outside); len(got) != 0 {
			t.Errorf("wrote %v outside the output directory", got)
		}
	})
	t.Run("stored file", func(t *testing.T) {
		// A stored schema outside is neither trusted nor removed, though
		// its checksum is the pinned one.
		out, outside := t.TempDir(), t.TempDir()
		target := filepath.Join(outside, "spec", "x", "schema.xsd.gz")
		writeGzip(t, target, schema)
		if err := os.Symlink(outside, filepath.Join(out, host)); err != nil {
			t.Fatal(err)
		}
		if _, err := up.fetcher(out).fetch(p); err == nil {
			t.Error("used a stored file outside the output directory")
		}
		if got := readGzip(t, target); got != schema {
			t.Errorf("the file outside is now %q", got)
		}
	})
	t.Run("link that stays inside", func(t *testing.T) {
		out := t.TempDir()
		if err := os.Mkdir(filepath.Join(out, "real"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real", filepath.Join(out, host)); err != nil {
			t.Fatal(err)
		}
		if data, err := up.fetcher(out).fetch(p); err != nil || string(data) != schema {
			t.Fatalf("got %q, %v", data, err)
		}
		if got := readGzip(t, filepath.Join(out, "real", "spec", "x", "schema.xsd.gz")); got != schema {
			t.Errorf("stored %q", got)
		}
	})

	// The output directory is made when it does not exist yet.
	out := filepath.Join(t.TempDir(), "new", "schemas")
	if _, err := up.fetcher(out).fetch(p); err != nil {
		t.Fatal(err)
	}
	if got := readGzip(t, stored(t, out, p)); got != schema {
		t.Errorf("stored %q", got)
	}
}

// A schema with the pinned checksum is stored compressed, and a second run
// takes it from the directory without asking upstream.
func TestFetchStoresAndReusesPinnedSchema(t *testing.T) {
	up := newUpstream(t, map[string]string{schemaPath: schema})
	out := t.TempDir()
	p := up.pin(t, schemaPath, sum(schema))
	for run := range 2 {
		data, err := up.fetcher(out).fetch(p)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != schema {
			t.Fatalf("run %d returned %q", run, data)
		}
	}
	if got := readGzip(t, stored(t, out, p)); got != schema {
		t.Fatalf("stored %q", got)
	}
	host := strings.TrimPrefix(up.URL, "https://")
	if got, want := files(t, out), []string{host + "/spec/x/schema.xsd.gz"}; !slices.Equal(got, want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
	if n := up.requests.Load(); n != 1 {
		t.Fatalf("%d requests, want 1: the second run must reuse the stored file", n)
	}
}

// What upstream serves under another checksum than the pinned one is
// refused, and nothing of it reaches the directory.
func TestFetchRejectsChangedUpstream(t *testing.T) {
	up := newUpstream(t, map[string]string{schemaPath: schema + "<!-- changed -->"})
	out := t.TempDir()
	data, err := up.fetcher(out).fetch(up.pin(t, schemaPath, sum(schema)))
	wantError(t, err, "checksum mismatch")
	wantError(t, err, "lock file has "+sum(schema))
	if data != nil {
		t.Fatalf("returned %q with a checksum mismatch", data)
	}
	if got := files(t, out); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}
}

// A stored file that does not have the pinned checksum is never used: it is
// fetched again, and gone when upstream does not have the pinned file either.
func TestFetchDistrustsStoredFile(t *testing.T) {
	const tampered = schema + "<!-- tampered -->"
	for name, store := range map[string]func(t *testing.T, name string){
		"other content": func(t *testing.T, name string) { writeGzip(t, name, tampered) },
		"not gzip":      func(t *testing.T, name string) { writeFile(t, name, tampered) },
		"truncated": func(t *testing.T, name string) {
			writeGzip(t, name, schema)
			data, _ := os.ReadFile(name)
			writeFile(t, name, string(data[:len(data)-6]))
		},
	} {
		t.Run(name+"/upstream pinned", func(t *testing.T) {
			up := newUpstream(t, map[string]string{schemaPath: schema})
			out := t.TempDir()
			p := up.pin(t, schemaPath, sum(schema))
			store(t, stored(t, out, p))
			data, err := up.fetcher(out).fetch(p)
			if err != nil || string(data) != schema {
				t.Fatalf("got %q, %v", data, err)
			}
			if got := readGzip(t, stored(t, out, p)); got != schema {
				t.Fatalf("stored %q", got)
			}
			if n := up.requests.Load(); n != 1 {
				t.Fatalf("%d requests, want 1", n)
			}
		})
		t.Run(name+"/upstream changed", func(t *testing.T) {
			up := newUpstream(t, map[string]string{schemaPath: tampered})
			out := t.TempDir()
			p := up.pin(t, schemaPath, sum(schema))
			store(t, stored(t, out, p))
			_, err := up.fetcher(out).fetch(p)
			wantError(t, err, "checksum mismatch")
			if got := files(t, out); len(got) != 0 {
				t.Fatalf("left %v to be embedded", got)
			}
		})
	}
}

func TestFetchSizeCap(t *testing.T) {
	up := newUpstream(t, map[string]string{schemaPath: schema})
	out := t.TempDir()
	p := up.pin(t, schemaPath, sum(schema))

	f := up.fetcher(out)
	f.maxBytes = len(schema) - 1
	_, err := f.fetch(p)
	wantError(t, err, "schema larger than")
	if got := files(t, out); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}

	// Exactly the limit is accepted.
	f.maxBytes = len(schema)
	if _, err := f.fetch(p); err != nil {
		t.Fatal(err)
	}

	// A stored file over the limit is not read to the end: it is fetched
	// again, and refused again.
	f.maxBytes = len(schema) - 1
	before := up.requests.Load()
	_, err = f.fetch(p)
	wantError(t, err, "schema larger than")
	if up.requests.Load() != before+1 {
		t.Fatal("a stored file over the limit was reused")
	}
}

func TestFetchHTTPError(t *testing.T) {
	up := newUpstream(t, nil)
	out := t.TempDir()
	_, err := up.fetcher(out).fetch(up.pin(t, "/missing.xsd", sum(schema)))
	wantError(t, err, "HTTP 404")
	if got := files(t, out); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}
}

// A URL whose host and path leave the output directory is refused before
// anything is requested or written.
func TestFetchRejectsPathOutsideOutput(t *testing.T) {
	up := newUpstream(t, map[string]string{"/": schema})
	parent := t.TempDir()
	out := filepath.Join(parent, "out")
	if err := os.Mkdir(out, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, urlPath := range []string{"/../../escape.xsd", "/../../../../../../tmp/escape.xsd", "/a/../../../escape.xsd"} {
		_, err := up.fetcher(out).fetch(up.pin(t, urlPath, sum(schema)))
		wantError(t, err, "does not map to a local path")
	}
	if n := up.requests.Load(); n != 0 {
		t.Fatalf("%d requests for refused URLs", n)
	}
	if got := files(t, parent); len(got) != 0 {
		t.Fatalf("wrote %v", got)
	}

	// Dots that stay inside are only cleaned.
	p := up.pin(t, "/a/../b.xsd", sum(schema))
	if name, err := localName(p); err != nil || filepath.ToSlash(name) != p.url.Host+"/b.xsd.gz" {
		t.Fatalf("got %q, %v", name, err)
	}
}

// Redirects are followed only over https and on the pinned host, however
// right the checksum of what the other end serves.
func TestRedirects(t *testing.T) {
	otherHTTPS := newUpstream(t, map[string]string{schemaPath: schema})
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, schema) }))
	t.Cleanup(plain.Close)
	redirect := func(to string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
	}
	up := newUpstream(t, map[string]string{schemaPath: schema}, func(mux *http.ServeMux) {
		mux.HandleFunc("/same-host.xsd", redirect(schemaPath))
		mux.HandleFunc("/other-host.xsd", redirect(otherHTTPS.URL+schemaPath))
		mux.HandleFunc("/plain.xsd", redirect(plain.URL+schemaPath))
		mux.HandleFunc("/loop.xsd", redirect("/loop.xsd"))
	})

	out := t.TempDir()
	if data, err := up.fetcher(out).fetch(up.pin(t, "/same-host.xsd", sum(schema))); err != nil || string(data) != schema {
		t.Fatalf("redirect on the same host: got %q, %v", data, err)
	}
	for urlPath, want := range map[string]string{
		"/other-host.xsd": "refused: only https on",
		"/plain.xsd":      "refused: only https on",
		"/loop.xsd":       "redirects",
	} {
		out := t.TempDir()
		_, err := up.fetcher(out).fetch(up.pin(t, urlPath, sum(schema)))
		wantError(t, err, want)
		if got := files(t, out); len(got) != 0 {
			t.Errorf("%s: wrote %v", urlPath, got)
		}
	}
	if n := otherHTTPS.requests.Load(); n != 0 {
		t.Fatalf("%d requests reached the other host", n)
	}
}

func TestPrune(t *testing.T) {
	out := t.TempDir()
	pinOf := func(raw string) pin {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return pin{url: u}
	}
	pins := []pin{pinOf("https://purl.example.org/spec/a/keep.xsd"), pinOf("https://other.example.org/b/keep.xsd")}
	for _, name := range []string{
		"purl.example.org/spec/a/keep.xsd.gz",
		"purl.example.org/spec/a/dropped.xsd.gz",
		"purl.example.org/spec/old/dropped.xsd.gz",
		"purl.example.org/spec/old/deep/er/dropped.xsd.gz",
		"purl.example.org/notes.txt",
		"other.example.org/b/keep.xsd.gz",
		"unlisted.example.org/x.xsd.gz",
		"schemas.lock",
		"schematron.json.gz",
	} {
		writeFile(t, filepath.Join(out, filepath.FromSlash(name)), "x")
	}
	outside := filepath.Join(t.TempDir(), "outside.xsd.gz")
	writeFile(t, outside, "x")
	if err := os.Symlink(outside, filepath.Join(out, "purl.example.org", "spec", "a", "link.xsd.gz")); err != nil {
		t.Fatal(err)
	}

	removed, err := prune(out, pins)
	if err != nil {
		t.Fatal(err)
	}
	wantRemoved := []string{
		"purl.example.org/notes.txt",
		"purl.example.org/spec/a/dropped.xsd.gz",
		"purl.example.org/spec/a/link.xsd.gz",
		"purl.example.org/spec/old/deep/er/dropped.xsd.gz",
		"purl.example.org/spec/old/dropped.xsd.gz",
	}
	for i := range removed {
		removed[i] = filepath.ToSlash(removed[i])
	}
	if !slices.Equal(removed, wantRemoved) {
		t.Errorf("removed %v, want %v", removed, wantRemoved)
	}
	// The directories of other hosts and the files beside them are not
	// this command's to remove.
	wantLeft := []string{
		"other.example.org/b/keep.xsd.gz",
		"purl.example.org/spec/a/keep.xsd.gz",
		"schemas.lock",
		"schematron.json.gz",
		"unlisted.example.org/x.xsd.gz",
	}
	if got := files(t, out); !slices.Equal(got, wantLeft) {
		t.Errorf("left %v, want %v", got, wantLeft)
	}
	if _, err := os.Stat(filepath.Join(out, "purl.example.org", "spec", "old")); !os.IsNotExist(err) {
		t.Errorf("the emptied directory is left: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Errorf("the target of a removed link is gone: %v", err)
	}

	// A second run finds nothing.
	if removed, err := prune(out, pins); err != nil || len(removed) != 0 {
		t.Errorf("second run removed %v, %v", removed, err)
	}
}

// The whole command: the lock file's schemas are fetched, a schema the lock
// file no longer lists is removed, and the rules are compiled.
func TestRun(t *testing.T) {
	const second = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"/>`
	up := newUpstream(t, map[string]string{schemaPath: schema, "/spec/y/second.xsd": second})
	host := strings.TrimPrefix(up.URL, "https://")
	dir := t.TempDir()
	out := filepath.Join(dir, "schemas")
	lock := filepath.Join(out, "schemas.lock")
	rulesPath := filepath.Join(dir, "rules.sch")
	writeFile(t, rulesPath, rules)
	writeFile(t, lock, sum(schema)+" "+up.URL+schemaPath+"\n"+sum(second)+" "+up.URL+"/spec/y/second.xsd\n")

	if err := run(up.fetcher(out), lock, rulesPath); err != nil {
		t.Fatal(err)
	}
	want := []string{host + "/spec/x/schema.xsd.gz", host + "/spec/y/second.xsd.gz", "schemas.lock", rulesFile}
	if got := files(t, out); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	var compiled map[string]any
	if err := json.Unmarshal([]byte(readGzip(t, filepath.Join(out, rulesFile))), &compiled); err != nil || len(compiled) == 0 {
		t.Fatalf("compiled rules: %v, %v", compiled, err)
	}

	// The second schema leaves the lock file: its file goes, without a
	// request for the one that stays.
	writeFile(t, lock, sum(schema)+" "+up.URL+schemaPath+"\n")
	before := up.requests.Load()
	if err := run(up.fetcher(out), lock, rulesPath); err != nil {
		t.Fatal(err)
	}
	want = []string{host + "/spec/x/schema.xsd.gz", "schemas.lock", rulesFile}
	if got := files(t, out); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if up.requests.Load() != before {
		t.Fatal("the stored schema was requested again")
	}

	// A failed checksum stops the run before anything is pruned or compiled.
	if err := os.Remove(filepath.Join(out, rulesFile)); err != nil {
		t.Fatal(err)
	}
	writeFile(t, lock, sum(schema)+" "+up.URL+schemaPath+"\n"+sum("other")+" "+up.URL+"/spec/y/second.xsd\n")
	err := run(up.fetcher(out), lock, rulesPath)
	wantError(t, err, "checksum mismatch")
	wantError(t, err, "/spec/y/second.xsd")
	want = []string{host + "/spec/x/schema.xsd.gz", "schemas.lock"}
	if got := files(t, out); !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	broken := filepath.Join(dir, "broken.sch")
	writeFile(t, broken, `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron"><sch:pattern></sch:schema>`)
	for name, tc := range map[string]struct{ lock, rules, want string }{
		"missing lock":  {filepath.Join(dir, "none.lock"), rulesPath, "none.lock"},
		"missing rules": {"", filepath.Join(dir, "none.sch"), "none.sch"},
		"broken rules":  {"", broken, "broken.sch"},
	} {
		t.Run(name, func(t *testing.T) {
			writeFile(t, lock, sum(schema)+" "+up.URL+schemaPath+"\n")
			if tc.lock == "" {
				tc.lock = lock
			}
			wantError(t, run(up.fetcher(out), tc.lock, tc.rules), tc.want)
		})
	}
}
