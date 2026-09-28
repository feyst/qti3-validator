// Command fetchschemas downloads the QTI schemas pinned in
// internal/validator/schemas/schemas.lock, verifies their checksums, and
// compiles the Schematron rules embedded in them, together with the
// validator's own rules in rules/qti3-additional-checks.sch, to
// schematron.json.gz. A schema already downloaded with the pinned checksum
// is reused, so changing a rule needs no network.
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
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/kennisnet/qti3-validator/internal/schematron"
	"github.com/kennisnet/qti3-validator/internal/xmlenc"
)

const maxSchemaBytes = 64 << 20

type pin struct {
	sha256 string
	url    *url.URL
}

func main() {
	lockPath := flag.String("lock", "internal/validator/schemas/schemas.lock", "lock file with sha256 and URL per schema")
	outDir := flag.String("out", "internal/validator/schemas", "directory to store the schemas in")
	extraRules := flag.String("rules", "rules/qti3-additional-checks.sch", "the validator's own Schematron rules")
	flag.Parse()

	pins, err := readLock(*lockPath)
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var ruleSets []schematron.RuleSet
	for _, p := range pins {
		data, err := fetch(client, p, *outDir)
		if err != nil {
			log.Fatalf("%s: %v", p.url, err)
		}
		log.Printf("schema %s", p.url)
		rs, err := extractRules(p.url.String(), data)
		if err != nil {
			log.Fatal(err)
		}
		if len(rs.Patterns) > 0 {
			ruleSets = append(ruleSets, rs)
			log.Printf("extracted %d Schematron patterns from %s", len(rs.Patterns), p.url)
		}
	}
	extra, err := os.ReadFile(*extraRules)
	if err != nil {
		log.Fatal(err)
	}
	rs, err := extractRules(filepath.Base(*extraRules), extra)
	if err != nil {
		log.Fatal(err)
	}
	ruleSets = append(ruleSets, rs)
	log.Printf("extracted %d Schematron patterns from %s", len(rs.Patterns), *extraRules)
	// Compile the rules here, at build time; the image ships only the result.
	compiled, err := schematron.Precompile(ruleSets...)
	if err != nil {
		log.Fatal(err)
	}
	if err := writeRules(filepath.Join(*outDir, rulesFile), compiled); err != nil {
		log.Fatal(err)
	}
	log.Printf("compiled Schematron rules to %s", rulesFile)
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
	return os.WriteFile(name, buf.Bytes(), 0o644)
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
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			return nil, fmt.Errorf("%s:%d: want \"<sha256> <url>\"", name, line)
		}
		u, err := url.Parse(fields[1])
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("%s:%d: want an https URL", name, line)
		}
		pins = append(pins, pin{sha256: fields[0], url: u})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(pins) == 0 {
		return nil, errors.New(name + ": no schemas listed")
	}
	return pins, nil
}

// fetch downloads one schema, verifies it, stores it compressed and returns
// its bytes.
func fetch(client *http.Client, p pin, outDir string) ([]byte, error) {
	rel := path.Clean(p.url.Host + p.url.Path)
	if !filepath.IsLocal(rel) {
		return nil, errors.New("URL does not map to a local path")
	}
	dst := filepath.Join(outDir, filepath.FromSlash(rel)+".gz")
	if data, ok := cached(dst, p.sha256); ok {
		return data, nil
	}
	resp, err := client.Get(p.url.String())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSchemaBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSchemaBytes {
		return nil, errors.New("schema larger than 64 MiB")
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
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, err
	}
	return data, os.WriteFile(dst, gz.Bytes(), 0o644)
}

// cached returns a schema stored by an earlier run, if its checksum is the
// pinned one.
func cached(name, sha string) ([]byte, bool) {
	f, err := os.Open(name)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(zr, maxSchemaBytes+1))
	if err != nil || len(data) > maxSchemaBytes {
		return nil, false
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]) == sha
}
