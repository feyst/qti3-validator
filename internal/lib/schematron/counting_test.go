package schematron

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/lib/xpath"
)

const ownRules = "../../../rules/qti3-additional-checks.sch"

func extractOwnRules(t testing.TB) RuleSet {
	t.Helper()
	f, err := os.Open(ownRules)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rs, err := Extract(ownRules, f)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// compileOwnRules compiles the additional checks, with counts by identifier
// indexed or as plain XPath.
func compileOwnRules(t testing.TB, indexed bool) *Engine {
	t.Helper()
	rs := extractOwnRules(t)
	indexedCounts = indexed
	defer func() { indexedCounts = true }()
	e, err := Compile(rs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestIndexCounts(t *testing.T) {
	ns := map[string]string{"q": "urn:q", "x": "urn:x"}
	for in, want := range map[string]string{
		"count(../*[self::q:a or self::b][@id = current()/@id]) = 1":                              "qtiv:count-by-attribute(.., 'child', '{urn:q}a {}b', 'id', current()/@id) = 1",
		"count($s//q:*[self::q:a\n  or self::x:b][@id = current()/@id]) &lt;= 1":                  "qtiv:count-by-attribute($s, 'descendant', '{urn:q}a', 'id', current()/@id) &lt;= 1",
		"not(count(//*[self::q:a][@id=current()/@id])=1)":                                         "not(qtiv:count-by-attribute(/, 'descendant', '{urn:q}a', 'id', current()/@id)=1)",
		"count(/q:*[self::q:a][@id = current()/@id]) + count(../*[self::q:b][@k = current()/@k])": "qtiv:count-by-attribute(/, 'child', '{urn:q}a', 'id', current()/@id) + qtiv:count-by-attribute(.., 'child', '{urn:q}b', 'k', current()/@k)",
		// Left as they are.
		"count(../*[self::q:a][@id = current()/@other])":     "",
		"count(../*[self::q:a][@id = @id])":                  "",
		"count(../*[self::z:a][@id = current()/@id])":        "",
		"count(../q:a[@id = current()/@id])":                 "",
		"count(../*[self::q:a][1][@id = current()/@id])":     "",
		"count(a/*[self::q:a][@id = current()/@id])":         "",
		"xcount(../*[self::q:a][@id = current()/@id])":       "",
		"'count(../*[self::q:a][@id = current()/@id])'":      "",
		"count(../*[self::q:a or q:b][@id = current()/@id])": "",
	} {
		if want == "" {
			want = in
		}
		if got := indexCounts(in, ns, "qtiv"); got != want {
			t.Errorf("indexCounts(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

// The indexed counts bind a prefix of their own to the extension
// namespace, one the schema does not use; the schema's own expressions
// cannot call the extension function by its name.
func TestIndexedCountsBindTheirOwnPrefix(t *testing.T) {
	ruleSet := func(ns map[string]string, test string) RuleSet {
		return RuleSet{Source: "s", Namespaces: ns, Patterns: []Pattern{{ID: "p", Rules: []Rule{{
			Context:    "q:a",
			Assertions: []Assertion{{Test: test, Message: []MessagePart{{Text: "duplicate"}}}},
		}}}}}
	}
	const unique = "count(//q:*[self::q:a][@id = current()/@id]) = 1"
	for _, tc := range []struct {
		ns   map[string]string
		want string // prefix of the call
	}{
		{map[string]string{"q": "urn:q"}, "qtiv"},
		{map[string]string{"q": "urn:q", "qtiv": "urn:other", "qtiv2": "urn:other2"}, "qtiv3"},
		{map[string]string{"q": "urn:q", "e": xpath.ExtensionNamespace}, "e"},
	} {
		c, err := Precompile(ruleSet(tc.ns, unique))
		if err != nil {
			t.Fatal(err)
		}
		test := c.Sets[0].Patterns[0].Rules[0].Assertions[0].Test
		if !strings.HasPrefix(test, tc.want+":count-by-attribute(") {
			t.Errorf("namespaces %v: test %s", tc.ns, test)
		}
		for p, uri := range tc.ns {
			if c.Sets[0].Namespaces[p] != uri {
				t.Errorf("namespaces %v: prefix %s rebound in %v", tc.ns, p, c.Sets[0].Namespaces)
			}
		}
		e, err := New(c)
		if err != nil {
			t.Fatal(err)
		}
		failures, err := e.Validate(nil, parseDoc(t, `<r xmlns="urn:q"><a id="x"/><a id="y"/><a id="x"/></r>`), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(failures) != 2 {
			t.Errorf("namespaces %v: %d failures, want 2", tc.ns, len(failures))
		}
	}
	for _, ns := range []map[string]string{{"q": "urn:q"}, {"q": "urn:q", "qtiv": "urn:other"}} {
		if _, err := Precompile(ruleSet(ns, "qtiv:count-by-attribute(/, 'descendant', '{urn:q}a', 'id', current()/@id) = 1")); err == nil {
			t.Errorf("namespaces %v: a rule calls the extension function", ns)
		}
	}
}

// The uniqueness checks of the additional rules use indexed counts.
func TestOwnRulesUseIndexedCounts(t *testing.T) {
	rs := extractOwnRules(t)
	c, err := Precompile(rs)
	if err != nil {
		t.Fatal(err)
	}
	indexed := map[string]int{}
	for _, cs := range c.Sets {
		for _, p := range cs.Patterns {
			for _, r := range p.Rules {
				for _, a := range r.Assertions {
					if strings.Contains(a.Test, "qtiv:count-by-attribute(") {
						indexed[p.ID]++
					}
					if strings.Contains(a.Test, "count(") && strings.Contains(a.Test, "= current()/@identifier]") {
						t.Errorf("pattern %s: count left as XPath: %s", p.ID, a.Test)
					}
				}
			}
		}
	}
	if indexed["identifier-unique"] != 2 || indexed["test-identifier-unique"] != 1 {
		t.Fatalf("indexed counts per pattern: %v", indexed)
	}
}

// The additional rules must report the same findings with indexed counts as
// with plain XPath: same pattern, message and node, in the same order.
func TestIndexedCountsMatchXPath(t *testing.T) {
	indexed, plain := compileOwnRules(t, true), compileOwnRules(t, false)
	docs := slices.Clone(uniquenessDocs)
	r := rand.New(rand.NewPCG(1, 2))
	for range 150 {
		docs = append(docs, randomDoc(r))
	}
	// Deeply nested interactions, with identifiers that repeat across
	// levels and within one.
	var deep strings.Builder
	deep.WriteString(itemOpen + `<qti-item-body>`)
	for i := range 40 {
		fmt.Fprintf(&deep, `<qti-custom-interaction><qti-simple-choice identifier="%c"/><div><qti-gap identifier="%c"/></div>`, 'a'+i%3, 'a'+i%5)
	}
	deep.WriteString(strings.Repeat(`</qti-custom-interaction>`, 40) + `</qti-item-body></qti-assessment-item>`)
	docs = append(docs, deep.String())
	findings := 0
	for i, src := range docs {
		doc := parseDoc(t, src)
		want, err := plain.Validate(nil, doc, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, err := indexed.Validate(nil, doc, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, want, func(a, b Failure) bool {
			return a.Pattern == b.Pattern && a.Message == b.Message && a.Node == b.Node && a.Warning == b.Warning
		}) {
			t.Fatalf("document %d:\n%s\nindexed: %s\nXPath:   %s", i, src, describe(got), describe(want))
		}
		for _, f := range want {
			if strings.HasSuffix(f.Pattern, "identifier-unique") {
				findings++
			}
		}
	}
	t.Logf("%d documents, %d uniqueness findings", len(docs), findings)
	if findings < 200 {
		t.Fatalf("only %d uniqueness findings; the documents do not exercise the checks", findings)
	}
}

func describe(fs []Failure) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "\n  %s %s: %s", f.Pattern, f.Node.Path(), strings.Join(strings.Fields(f.Message), " "))
	}
	return b.String()
}

const (
	qtiNS    = `xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" xmlns:x="urn:x"`
	itemOpen = `<qti-assessment-item ` + qtiNS + ` identifier="i" title="t">`
	testOpen = `<qti-assessment-test ` + qtiNS + ` identifier="T" title="t">`
)

// uniquenessDocs make the uniqueness checks fire, and not fire, in each
// way the XPath allows.
var uniquenessDocs = []string{
	// Choices: within one interaction, across the nine kinds, not across
	// interactions.
	itemOpen + `<qti-item-body><qti-choice-interaction><qti-simple-choice identifier="a"/><qti-simple-choice identifier="b"/><qti-simple-choice identifier="a"/><qti-simple-choice identifier="a"/></qti-choice-interaction></qti-item-body></qti-assessment-item>`,
	itemOpen + `<qti-item-body><qti-gap-match-interaction><qti-gap-text identifier="g"/><qti-gap-img identifier="g"/><div><qti-gap identifier="g"/></div><qti-gap identifier="h"/></qti-gap-match-interaction>` +
		`<qti-hottext-interaction><p><qti-hottext identifier="g"/><qti-hottext identifier="k"/></p></qti-hottext-interaction>` +
		`<qti-inline-choice-interaction><qti-inline-choice identifier="k"/><qti-hotspot-choice identifier="k"/><qti-simple-associable-choice identifier="k"/><qti-associable-hotspot identifier="k"/></qti-inline-choice-interaction></qti-item-body></qti-assessment-item>`,
	// Nested interactions: a choice counts in every interaction around it.
	itemOpen + `<qti-item-body><qti-match-interaction><qti-simple-choice identifier="a"/><qti-custom-interaction><qti-simple-choice identifier="a"/><qti-portable-custom-interaction><qti-simple-choice identifier="a"/><qti-simple-choice identifier="b"/></qti-portable-custom-interaction><qti-simple-choice identifier="b"/></qti-custom-interaction></qti-match-interaction></qti-item-body></qti-assessment-item>`,
	itemOpen + `<qti-item-body><qti-x-interaction><qti-simple-choice identifier="c"/><qti-y-interaction><qti-simple-choice identifier="c"/></qti-y-interaction><qti-y-interaction><qti-simple-choice identifier="d"/></qti-y-interaction><qti-simple-choice identifier="d"/></qti-x-interaction></qti-item-body></qti-assessment-item>`,
	// Choices outside an interaction, without or with an empty identifier,
	// in another namespace or with a namespaced identifier.
	itemOpen + `<qti-item-body><qti-simple-choice identifier="a"/><qti-simple-choice identifier="a"/><qti-choice-interaction><qti-simple-choice/><qti-simple-choice/><qti-simple-choice identifier=""/><qti-simple-choice identifier=""/>` +
		`<x:qti-simple-choice identifier="e"/><qti-simple-choice identifier="e"/><qti-simple-choice x:identifier="f"/><qti-simple-choice identifier="f"/><x:qti-choice-interaction><qti-simple-choice identifier="e"/></x:qti-choice-interaction></qti-choice-interaction></qti-item-body></qti-assessment-item>`,
	// Declarations: across the four kinds, in items and tests, not in other
	// places or with other siblings.
	itemOpen + `<qti-response-declaration identifier="R"/><qti-outcome-declaration identifier="R"/><qti-template-declaration identifier="T"/><qti-context-declaration identifier="T"/><qti-outcome-declaration identifier="S"/><qti-outcome-declaration identifier="S"/><qti-outcome-declaration identifier="U"/><qti-stylesheet identifier="U"/><qti-outcome-declaration/><qti-outcome-declaration/><x:qti-outcome-declaration identifier="V"/><qti-outcome-declaration identifier="V"/>` +
		`<qti-item-body><qti-outcome-declaration identifier="W"/><qti-outcome-declaration identifier="W"/></qti-item-body></qti-assessment-item>`,
	testOpen + `<qti-outcome-declaration identifier="S"/><qti-context-declaration identifier="S"/><qti-outcome-declaration identifier="X"/><qti-response-declaration identifier="X"/><qti-template-declaration identifier="Y"/><qti-template-declaration identifier="Y"/>` +
		`<qti-test-part identifier="P"><qti-assessment-section identifier="S1"/></qti-test-part></qti-assessment-test>`,
	// Test parts, sections and references: across kinds and levels, and
	// against parts of another test in the same document.
	testOpen + `<qti-test-part identifier="P"><qti-assessment-section identifier="P"><qti-assessment-item-ref identifier="A"/><qti-assessment-section identifier="B"><qti-assessment-item-ref identifier="A"/><qti-assessment-section-ref identifier="B"/></qti-assessment-section></qti-assessment-section>` +
		`<qti-assessment-item-ref identifier="C"/><x:qti-assessment-item-ref identifier="C"/><qti-assessment-item-ref/><qti-assessment-item-ref/><qti-outcome-declaration identifier="C"/></qti-test-part></qti-assessment-test>`,
	`<x:wrapper ` + qtiNS + `><qti-assessment-test identifier="T"><qti-test-part identifier="P"/><qti-test-part identifier="Q"/></qti-assessment-test><qti-test-part identifier="P"/><qti-assessment-test><qti-assessment-section identifier="Q"/></qti-assessment-test></x:wrapper>`,
}

// randomElements are the elements random documents are built from: the
// ones the uniqueness checks look at, and some they must not.
var randomElements = []string{
	"qti-simple-choice", "qti-inline-choice", "qti-hottext", "qti-hotspot-choice", "qti-simple-associable-choice",
	"qti-associable-hotspot", "qti-gap-text", "qti-gap-img", "qti-gap",
	"qti-choice-interaction", "qti-gap-match-interaction", "qti-custom-interaction", "qti-interaction",
	"qti-response-declaration", "qti-outcome-declaration", "qti-template-declaration", "qti-context-declaration",
	"qti-test-part", "qti-assessment-section", "qti-assessment-section-ref", "qti-assessment-item-ref",
	"qti-assessment-item", "qti-assessment-test", "div", "x:qti-simple-choice", "x:qti-choice-interaction",
}

// randomDoc returns a document of random elements with identifiers from a
// small set, so that duplicates are common.
func randomDoc(r *rand.Rand) string {
	var b strings.Builder
	root := "qti-assessment-item"
	if r.IntN(3) == 0 {
		root = "qti-assessment-test"
	}
	b.WriteString("<" + root + " " + qtiNS + ">")
	var children func(depth int)
	children = func(depth int) {
		for range r.IntN(6) {
			name := randomElements[r.IntN(len(randomElements))]
			b.WriteString("<" + name)
			switch r.IntN(8) {
			case 0: // no identifier
			case 1:
				b.WriteString(` identifier=""`)
			case 2:
				b.WriteString(` x:identifier="a"`)
			default:
				fmt.Fprintf(&b, ` identifier="%c"`, 'a'+r.IntN(4))
			}
			b.WriteString(">")
			if depth < 5 {
				children(depth + 1)
			}
			b.WriteString("</" + name + ">")
		}
	}
	children(0)
	b.WriteString("</" + root + ">")
	return b.String()
}

// nestedInteractions opens depth interactions, one inside the other, each
// with a choice of its own identifier.
func nestedInteractions(depth int) string {
	var b strings.Builder
	for i := range depth {
		fmt.Fprintf(&b, `<qti-custom-interaction><qti-simple-choice identifier="n%d"/>`, i)
	}
	return b.String()
}

// One interaction with many choices, or a test with many references, must
// not take time quadratic in their number.
func TestIndexedCountsScale(t *testing.T) {
	e := compileOwnRules(t, true)
	for _, tc := range []struct {
		name, open, each, close string
		dups                    int
	}{
		{"choices", itemOpen + `<qti-item-body><qti-choice-interaction>`, `<qti-simple-choice identifier="c%d"/>`, `</qti-choice-interaction></qti-item-body></qti-assessment-item>`, 0},
		{"declarations", itemOpen, `<qti-outcome-declaration identifier="O%d"/>`, `</qti-assessment-item>`, 0},
		{"item references", testOpen + `<qti-test-part identifier="P"><qti-assessment-section identifier="S">`, `<qti-assessment-item-ref identifier="I%d"/>`, `</qti-assessment-section></qti-test-part></qti-assessment-test>`, 0},
		{"duplicate choices", itemOpen + `<qti-item-body><qti-choice-interaction>`, `<qti-simple-choice identifier="c%d"/>`, `</qti-choice-interaction></qti-item-body></qti-assessment-item>`, 3},
		// Each choice counts in its own interaction: 200 scopes, nested.
		{"nested interactions", itemOpen + `<qti-item-body>` + nestedInteractions(200), `<qti-simple-choice identifier="c%d"/>`, strings.Repeat(`</qti-custom-interaction>`, 200) + `</qti-item-body></qti-assessment-item>`, 2},
	} {
		const n = 20000
		var b strings.Builder
		b.WriteString(tc.open)
		for i := range n {
			fmt.Fprintf(&b, tc.each, i)
		}
		for i := range tc.dups {
			fmt.Fprintf(&b, tc.each, i*1000)
		}
		b.WriteString(tc.close)
		doc := parseDoc(t, b.String())
		start := time.Now()
		failures, err := e.Validate(nil, doc, 0)
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		var unique []string
		for _, f := range failures {
			if strings.HasSuffix(f.Pattern, "identifier-unique") {
				unique = append(unique, f.Node.Path())
			}
		}
		t.Logf("%s: %d elements, %v, %d uniqueness findings", tc.name, n+tc.dups, took, len(unique))
		if len(unique) != 2*tc.dups {
			t.Errorf("%s: want %d uniqueness findings, got %v", tc.name, 2*tc.dups, unique)
		}
		if took > slowdown*5*time.Second { // quadratic took minutes
			t.Errorf("%s: took %v", tc.name, took)
		}
	}
}
