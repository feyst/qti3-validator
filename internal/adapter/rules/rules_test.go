package rules

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/adapter/schemastore"
	"github.com/kennisnet/qti3-validator/internal/domain/qti"
	"github.com/kennisnet/qti3-validator/internal/lib/schematron"
	"github.com/kennisnet/qti3-validator/internal/testutil"
)

var (
	compiledOnce sync.Once
	compiled     *schematron.Compiled
	compiledErr  error
)

// versionRules returns the built-in rules of a QTI version.
func versionRules(t *testing.T, name string) *Checker {
	t.Helper()
	compiledOnce.Do(func() { compiled, compiledErr = schemastore.Embedded().Rules() })
	if compiledErr != nil {
		t.Fatal(compiledErr)
	}
	v, ok := qti.LookupVersion(name)
	if !ok {
		t.Fatalf("no QTI %s", name)
	}
	c, err := ForVersion(compiled, v)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// messages runs c on a file and returns the messages of the findings of the
// additional checks (own) or of the other rules, normalised and sorted. A
// warning is prefixed with "[warning] " when markWarnings.
func messages(t *testing.T, c *Checker, file string, own, markWarnings bool) ([]string, map[string]bool) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := c.CheckRules(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	patterns := map[string]bool{}
	for _, f := range findings {
		pattern, isOwn := strings.CutPrefix(f.Rule, AdditionalChecks+"#")
		if isOwn != own {
			continue
		}
		patterns[pattern] = true
		msg := strings.Join(strings.Fields(f.Message), " ")
		if markWarnings && f.Warning {
			msg = "[warning] " + msg
		}
		got = append(got, msg)
	}
	slices.Sort(got)
	return got, patterns
}

func readExpected(t *testing.T, name string) map[string][]string {
	t.Helper()
	var expected map[string][]string
	if err := json.Unmarshal(testutil.ReadTestdata(t, name), &expected); err != nil {
		t.Fatal(err)
	}
	return expected
}

// TestMatchesReference checks the rules embedded in the QTI 3.0.0 and LOM
// schemas against results of the ISO Schematron reference implementation
// (the XSLT skeleton, run by lxml 5 / libxml2 2.14.6 / libxslt) on generated
// documents. The documents were made by testdata/schematron/generate.py and
// chosen to fire 68 different QTI rules; the reference ran the QTI 3.0.0 and
// LOM rules, so the findings of the additional checks are left out here.
func TestMatchesReference(t *testing.T) {
	c := versionRules(t, "3.0.0")
	for name, want := range readExpected(t, "schematron/expected.json") {
		got, _ := messages(t, c, testutil.Testdata("schematron/docs/"+name), false, false)
		if want == nil {
			want = []string{}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, want)
		}
	}
}

// TestAdditionalChecksMatchReference runs the validator's own rules on one
// document per check and compares the findings with those of the ISO
// Schematron reference implementation, made by
// testdata/additional-checks/reference.py. Every pattern must fire at least
// once, so a check cannot go untested.
func TestAdditionalChecksMatchReference(t *testing.T) {
	c := versionRules(t, qti.LatestVersion().Name)
	fired := map[string]bool{}
	for name, want := range readExpected(t, "additional-checks/expected.json") {
		got, patterns := messages(t, c, testutil.Testdata("additional-checks/docs/"+name), true, true)
		for p := range patterns {
			fired[p] = true
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, want)
		}
	}
	for _, p := range readAdditionalChecks(t).Patterns {
		if !fired[p.ID] {
			t.Errorf("pattern %s fires on no test document", p.ID)
		}
	}
}

// TestAdditionalChecksCompiled fails when rules/qti3-additional-checks.sch
// changed after the embedded rules were built.
func TestAdditionalChecksCompiled(t *testing.T) {
	fresh, err := schematron.Precompile(readAdditionalChecks(t))
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := schemastore.Embedded().Rules()
	if err != nil {
		t.Fatal(err)
	}
	built, err := embedded.Subset(AdditionalChecks)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fresh.Sets)
	b, _ := json.Marshal(built.Sets)
	if !bytes.Equal(a, b) {
		t.Fatal("rules/qti3-additional-checks.sch changed; run go run ./cmd/fetchschemas")
	}
}

func readAdditionalChecks(t *testing.T) schematron.RuleSet {
	t.Helper()
	f, err := os.Open(testutil.Path("rules/qti3-additional-checks.sch"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := schematron.Extract(AdditionalChecks, f)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// A chain skips nil checkers and limits errors across its rule sets.
func TestChain(t *testing.T) {
	if Chain(nil, nil) != nil {
		t.Fatal("a chain of nil checkers is not nil")
	}
	var none *Checker
	if f, err := none.CheckRules([]byte("<x/>"), 0); f != nil || err != nil {
		t.Fatalf("nil checker: %v, %v", f, err)
	}
	c := versionRules(t, "3.0.0")
	data := testutil.ReadTestdata(t, "invalid/schematron-unknown-attribute.xml")
	all, err := Chain(c, c).CheckRules(data, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("chain of two: %d findings, %v", len(all), err)
	}
	limited, _ := Chain(c, c).CheckRules(data, 1)
	if len(limited) != 1 {
		t.Fatalf("limited to 1: %d findings", len(limited))
	}
}
