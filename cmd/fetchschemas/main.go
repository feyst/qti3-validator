// Command fetchschemas downloads the QTI schemas pinned in
// internal/adapter/schemastore/schemas/schemas.lock, verifies their checksums, and
// compiles the Schematron rules embedded in them, together with the
// validator's own rules in rules/qti3-additional-checks.sch, to
// schematron.json.gz. A schema already downloaded with the pinned checksum
// is reused, so changing a rule needs no network. A file under a schema host's
// directory that the lock file does not list is removed, because the
// validator embeds those directories whole.
//
// Each file is stored gzip-compressed under its URL host and path plus ".gz",
// so the validator can map the absolute schemaLocation URLs in the XSDs to
// embedded files. Compression keeps the 18 MB of XSD text out of the binary
// and out of resident memory; the checksum covers the uncompressed upstream
// bytes.
package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"qti3-validator/internal/lib/schematron"
	"qti3-validator/internal/lib/xmlenc"
)

// maxSchemaBytes is the largest schema accepted, uncompressed.
const maxSchemaBytes = 64 << 20

// maxRedirects is the number of redirects followed for one schema.
const maxRedirects = 5

type pin struct {
	sha256 string
	url    *url.URL
}

// fetcher downloads schemas into outDir.
type fetcher struct {
	client   *http.Client
	outDir   string
	maxBytes int // a larger schema is refused
}

func main() {
	lockPath := flag.String("lock", "internal/adapter/schemastore/schemas/schemas.lock", "lock file with sha256 and URL per schema")
	outDir := flag.String("out", "internal/adapter/schemastore/schemas", "directory to store the schemas in")
	extraRules := flag.String("rules", "rules/qti3-additional-checks.sch", "the validator's own Schematron rules")
	flag.Parse()

	f := fetcher{client: newClient(&http.Client{Timeout: 2 * time.Minute}), outDir: *outDir, maxBytes: maxSchemaBytes}
	if err := run(f, *lockPath, *extraRules); err != nil {
		log.Fatal(err)
	}
}

// newClient returns base with the redirect policy of checkRedirect.
func newClient(base *http.Client) *http.Client {
	base.CheckRedirect = checkRedirect
	return base
}

// checkRedirect follows a redirect only over https and to the host the lock
// file names. The checksum already decides what is accepted; this keeps the
// build from asking any other host, or asking in the clear, at all.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return fmt.Errorf("more than %d redirects", maxRedirects)
	}
	if req.URL.Scheme != "https" || req.URL.Host != via[0].URL.Host {
		return fmt.Errorf("redirect to %s://%s refused: only https on %s is followed", req.URL.Scheme, req.URL.Host, via[0].URL.Host)
	}
	return nil
}

// run fetches the schemas of the lock file, removes the files the lock file
// no longer lists, and compiles the Schematron rules.
func run(f fetcher, lockPath, extraRules string) error {
	pins, err := readLock(lockPath)
	if err != nil {
		return err
	}
	var ruleSets []schematron.RuleSet
	for _, p := range pins {
		data, err := f.fetch(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p.url, err)
		}
		log.Printf("schema %s", p.url)
		rs, err := extractRules(p.url.String(), data)
		if err != nil {
			return err
		}
		if len(rs.Patterns) > 0 {
			ruleSets = append(ruleSets, rs)
			log.Printf("extracted %d Schematron patterns from %s", len(rs.Patterns), p.url)
		}
	}
	removed, err := prune(f.outDir, pins)
	if err != nil {
		return err
	}
	for _, name := range removed {
		log.Printf("removed %s: not in %s", name, lockPath)
	}
	extra, err := os.ReadFile(extraRules)
	if err != nil {
		return err
	}
	rs, err := extractRules(filepath.Base(extraRules), extra)
	if err != nil {
		return err
	}
	ruleSets = append(ruleSets, rs)
	log.Printf("extracted %d Schematron patterns from %s", len(rs.Patterns), extraRules)
	// Compile the rules here, at build time; the image ships only the result.
	compiled, err := schematron.Precompile(ruleSets...)
	if err != nil {
		return err
	}
	if err := writeRules(filepath.Join(f.outDir, rulesFile), compiled); err != nil {
		return err
	}
	log.Printf("compiled Schematron rules to %s", rulesFile)
	return nil
}

// rulesFile holds the compiled Schematron rules of all schemas.
const rulesFile = "schematron.json.gz"

// extractRules returns the Schematron rules embedded in a schema.
func extractRules(source string, data []byte) (schematron.RuleSet, error) {
	data, err := xmlenc.ToUTF8(data)
	if err != nil {
		return schematron.RuleSet{}, fmt.Errorf("%s: %w", source, err)
	}
	return schematron.Extract(source, bytes.NewReader(data))
}

func writeRules(name string, compiled *schematron.Compiled) error {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(zw).Encode(compiled); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return os.WriteFile(name, buf.Bytes(), 0o600)
}

func readLock(name string) ([]pin, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var pins []pin
	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s:%d: want \"<sha256> <url>\"", name, line)
		}
		// Hexadecimal in either case; compared in lower case.
		if sum, err := hex.DecodeString(fields[0]); err != nil || len(sum) != sha256.Size {
			return nil, fmt.Errorf("%s:%d: want \"<sha256> <url>\": the checksum is not 64 hexadecimal digits", name, line)
		}
		u, err := url.Parse(fields[1])
		if err != nil || u.Scheme != "https" || !validHost(u.Hostname()) {
			return nil, fmt.Errorf("%s:%d: want an https URL with a host name", name, line)
		}
		pins = append(pins, pin{sha256: strings.ToLower(fields[0]), url: u})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, errors.New(name + ": no schemas listed")
	}
	return pins, nil
}

// validHost reports whether host can name the directory of a schema host: a
// name with a dot in it, and more than dots. "." and ".." would be the
// output directory and its parent, and a name without a dot could be a file
// of the output directory, such as the lock file.
func validHost(host string) bool {
	return strings.Contains(host, ".") && strings.Trim(host, ".") != ""
}

// localName is the file a schema is stored in, relative to the output
// directory: its URL host and path plus ".gz".
func localName(p pin) (string, error) {
	if !validHost(p.url.Hostname()) {
		return "", errors.New("URL has no host name to store the schema under")
	}
	rel := path.Clean(p.url.Host + p.url.Path)
	if !filepath.IsLocal(rel) {
		return "", errors.New("URL does not map to a local path")
	}
	return filepath.FromSlash(rel) + ".gz", nil
}

// fetch downloads one schema, verifies it, stores it compressed and returns
// its bytes. A stored file with another checksum than the pinned one is
// removed, so it is never embedded, also when the download fails.
//
// The output directory is read and written through a root, like prune: a
// link in it that leads outside is an error, not a place to write.
func (f fetcher) fetch(p pin) ([]byte, error) {
	name, err := localName(p)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(f.outDir, 0o750); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(f.outDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if data, ok := cached(root, name, p.sha256, f.maxBytes); ok {
		return data, nil
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	resp, err := f.client.Get(p.url.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(f.maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > f.maxBytes {
		return nil, fmt.Errorf("schema larger than %d bytes", f.maxBytes)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != p.sha256 {
		return nil, fmt.Errorf("checksum mismatch: got %s, lock file has %s", got, p.sha256)
	}
	var gz bytes.Buffer
	zw, err := gzip.NewWriterLevel(&gz, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	if err := root.MkdirAll(filepath.Dir(name), 0o750); err != nil {
		return nil, err
	}
	return data, root.WriteFile(name, gz.Bytes(), 0o600)
}

// cached returns a schema stored by an earlier run, if its checksum is the
// pinned one.
func cached(root *os.Root, name, sha string, maxBytes int) ([]byte, bool) {
	f, err := root.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(zr, int64(maxBytes)+1))
	if err != nil || len(data) > maxBytes {
		return nil, false
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]) == sha
}

// prune removes what the lock file does not list from the directories of
// the hosts it names, and returns the files removed. The validator embeds
// those directories whole, so a schema dropped from the lock file, or any
// other file left there, would otherwise still end up in the binary. Only
// those directories are touched: the rest of outDir is not this command's.
func prune(outDir string, pins []pin) ([]string, error) {
	keep := map[string]bool{}
	hosts := map[string]bool{}
	for _, p := range pins {
		name, err := localName(p)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.url, err)
		}
		name = filepath.ToSlash(name)
		keep[name] = true
		host, _, _ := strings.Cut(name, "/")
		hosts[host] = true
	}
	// Through a root, so that nothing outside outDir is removed, whatever
	// links the directories hold.
	root, err := os.OpenRoot(outDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var removed []string
	for host := range hosts {
		stale, dirs, err := unlisted(root.FS(), host, keep)
		if err != nil {
			return removed, err
		}
		for _, name := range stale {
			if err := root.Remove(filepath.FromSlash(name)); err != nil {
				return removed, err
			}
			removed = append(removed, filepath.FromSlash(name))
		}
		// Deepest first; a directory that still holds a schema stays.
		for _, dir := range slices.Backward(dirs) {
			if entries, err := fs.ReadDir(root.FS(), dir); err == nil && len(entries) == 0 {
				if err := root.Remove(filepath.FromSlash(dir)); err != nil {
					return removed, err
				}
			}
		}
	}
	slices.Sort(removed)
	return removed, nil
}

// unlisted returns the files under dir that keep does not name, links
// included, and the directories under it, parents first.
func unlisted(fsys fs.FS, dir string, keep map[string]bool) (stale, dirs []string, err error) {
	err = fs.WalkDir(fsys, dir, func(name string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			dirs = append(dirs, name)
		case !keep[name]:
			stale = append(stale, name)
		}
		return nil
	})
	return stale, dirs, err
}
