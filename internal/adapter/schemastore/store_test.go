package schemastore

import (
	"bufio"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/testutil"
)

// Pinned returns the URLs in schemas.lock.
func pinned(t *testing.T) map[string]bool {
	t.Helper()
	f, err := os.Open(testutil.Path("internal/adapter/schemastore/schemas/schemas.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	urls := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			urls[fields[1]] = true
		}
	}
	return urls
}

// An override must target a pinned URL.
func TestOverridesArePinned(t *testing.T) {
	urls := pinned(t)
	for u := range overrides {
		if !urls[u] {
			t.Errorf("override %s is not in schemas.lock", u)
		}
	}
}

func TestOpen(t *testing.T) {
	s := Embedded()
	for u := range pinned(t) {
		if err := s.Has(u); err != nil {
			t.Fatal(err)
		}
		rc, err := s.Open(u)
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || !strings.Contains(string(data), "schema") {
			t.Fatalf("%s: %d bytes, %v", u, len(data), err)
		}
	}
	if err := s.Has("https://example.org/missing.xsd"); err == nil {
		t.Fatal("found a schema that is not embedded")
	}
	if _, err := s.Open("file:///etc/passwd"); err == nil {
		t.Fatal("opened a non-HTTP location")
	}
	if _, err := s.Rules(); err != nil {
		t.Fatal(err)
	}
}
