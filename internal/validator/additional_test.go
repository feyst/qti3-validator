package validator

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/schematron"
	"github.com/kennisnet/qti3-validator/internal/xpath"
)

const additionalChecksFile = "../../rules/qti3-additional-checks.sch"

// TestAdditionalChecksMatchReference runs the validator's own rules on one
// document per check and compares the findings with those of the ISO
// Schematron reference implementation, made by
// testdata/additional-checks/reference.py. Every pattern must fire at least
// once, so a check cannot go untested.
func TestAdditionalChecksMatchReference(t *testing.T) {
	data, err := os.ReadFile("../../testdata/additional-checks/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string][]string
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	rules := testValidator(t).versions[LatestVersion()].rules
	fired := map[string]bool{}
	for name, want := range expected {
		f, err := os.Open(filepath.Join("../../testdata/additional-checks/docs", name))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := xpath.Parse(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		failures, err := rules.Validate(doc, 0)
		if err != nil {
			t.Fatal(err)
		}
		got := []string{}
		for _, fl := range failures {
			if fl.Source != AdditionalRules {
				continue
			}
			fired[fl.Pattern] = true
			msg := strings.Join(strings.Fields(fl.Message), " ")
			if fl.Warning {
				msg = "[warning] " + msg
			}
			got = append(got, msg)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", name, got, want)
		}
	}
	for _, id := range additionalPatterns(t) {
		if !fired[id] {
			t.Errorf("pattern %s fires on no test document", id)
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
	embedded, err := loadRules(SchemaFS())
	if err != nil {
		t.Fatal(err)
	}
	built, err := embedded.Subset(AdditionalRules)
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
	f, err := os.Open(additionalChecksFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := schematron.Extract(AdditionalRules, f)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func additionalPatterns(t *testing.T) []string {
	var ids []string
	for _, p := range readAdditionalChecks(t).Patterns {
		ids = append(ids, p.ID)
	}
	return ids
}

// A finding of the validator's own rules names their file in the generator.
func TestAdditionalChecksGenerator(t *testing.T) {
	data, err := os.ReadFile("../../testdata/additional-checks/docs/response-declaration-exists.xml")
	if err != nil {
		t.Fatal(err)
	}
	r := DocumentReport(validateString(t, string(data), ValidateOptions{}), testMeta)
	var gens []string
	for _, it := range r.Errors {
		gens = append(gens, it.Generator)
	}
	if !slices.Contains(gens, "schematron|qti3-additional-checks.sch#response-declaration-exists") {
		t.Fatalf("generators %q", gens)
	}
}
