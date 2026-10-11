package rules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/schematron"
	"qti3-validator/internal/testutil"
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
	findings, err := c.CheckRules(context.Background(), data, 0)
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
// (the XSLT skeleton, run by lxml 5 / libxml2 2.14.6 / libxslt) on random
// documents, generated once from the names in the QTI 3.0 XSD and chosen to
// fire 68 different QTI rules; the reference ran the QTI 3.0.0 and LOM rules,
// so the findings of the additional checks are left out here.
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
	if f, err := none.CheckRules(context.Background(), []byte("<x/>"), 0); f != nil || err != nil {
		t.Fatalf("nil checker: %v, %v", f, err)
	}
	c := versionRules(t, "3.0.0")
	data := testutil.ReadTestdata(t, "invalid/schematron-unknown-attribute.xml")
	all, err := Chain(c, c).CheckRules(context.Background(), data, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("chain of two: %d findings, %v", len(all), err)
	}
	limited, _ := Chain(c, c).CheckRules(context.Background(), data, 1)
	if len(limited) != 1 {
		t.Fatalf("limited to 1: %d findings", len(limited))
	}
}

// The rule sets of a chain share one budget per document. When it runs out,
// the findings so far come back with app.ErrTooComplex.
func TestBudget(t *testing.T) {
	data := testutil.ReadTestdata(t, "invalid/schematron-unknown-attribute.xml")
	check := func(c *Checker, steps int64) ([]app.RuleFinding, error) {
		c.budget = budget{base: steps}
		return c.CheckRules(context.Background(), data, 0)
	}
	// Find the least budget one rule set needs.
	c := versionRules(t, "3.0.0")
	lo, hi := int64(0), int64(BaseSteps)
	for lo+1 < hi {
		mid := (lo + hi) / 2
		if _, err := check(c, mid); err != nil {
			lo = mid
		} else {
			hi = mid
		}
	}
	if _, err := check(c, lo); !errors.Is(err, app.ErrTooComplex) {
		t.Fatalf("got %v, want app.ErrTooComplex", err)
	}
	findings, err := check(Chain(c, c), hi)
	if !errors.Is(err, app.ErrTooComplex) || len(findings) != 1 {
		t.Fatalf("chain of two on the budget of one: %d findings, %v", len(findings), err)
	}
}

// quadraticRules compares every choice with every other: n² steps on an
// item with n choices.
const quadraticRules = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <sch:ns prefix="qti" uri="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>
  <sch:pattern id="QUADRATIC">
    <sch:rule context="qti:qti-simple-choice">
      <sch:assert test="count(//qti:qti-simple-choice[@identifier = current()/@identifier]) = 1">Identifiers repeat.</sch:assert>
    </sch:rule>
  </sch:pattern>
</sch:schema>`

// mountedRules returns a checker for the rules of a Schematron file.
func mountedRules(t *testing.T, src string) *Checker {
	t.Helper()
	set, err := schematron.Extract("test.sch", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := schematron.Compile(set)
	if err != nil {
		t.Fatal(err)
	}
	return Mounted(engine)
}

// The budget grows with the document: plain content passes at any size,
// and rules whose work grows faster than the document still run out. The
// budget and the document are a hundredth of the defaults and of
// MAX_REQUEST_SIZE, to stay fast under -race; TestLinearDocumentAtMaxSize
// in internal/app checks the defaults.
func TestBudgetGrowsWithDocument(t *testing.T) {
	scaled := budget{base: BaseSteps / 100, perNode: StepsPerNode}
	linear := []byte(testutil.LinearItem(int(qti.DefaultLimits.MaxDocumentSize / 100)))
	c := versionRules(t, "3.0.1")
	c.budget = scaled
	if findings, err := c.CheckRules(context.Background(), linear, 0); err != nil || len(findings) != 0 {
		t.Fatalf("plain content: %d findings, %v", len(findings), err)
	}
	// The base alone is not enough.
	c.budget = budget{base: scaled.base}
	if _, err := c.CheckRules(context.Background(), linear, 0); !errors.Is(err, app.ErrTooComplex) {
		t.Fatalf("plain content on the base alone: %v, want app.ErrTooComplex", err)
	}

	for name, tc := range map[string]struct{ rules, doc string }{
		"quadratic": {quadraticRules, testutil.ItemWithChoices(2000)},    // 48 million steps, 7 million allowed
		"cubic":     {testutil.SlowRules, testutil.ItemWithChoices(300)}, // 326 million steps, 2 million allowed
	} {
		c := mountedRules(t, tc.rules)
		c.budget = scaled
		if _, err := c.CheckRules(context.Background(), []byte(tc.doc), 0); !errors.Is(err, app.ErrTooComplex) {
			t.Errorf("%s: got %v, want app.ErrTooComplex", name, err)
		}
	}
}

func TestContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data := testutil.ReadTestdata(t, "invalid/schematron-unknown-attribute.xml")
	if _, err := versionRules(t, "3.0.0").CheckRules(ctx, data, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
