package bootstrap

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/adapter/rules"
	"qti3-validator/internal/app"
	"qti3-validator/internal/app/report"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/testutil"
)

// The scaling suite runs the real validator, end to end, on generated
// documents and packages of size n and 4n, and holds the work for the
// larger to that of a linear validator: four times the work of the smaller,
// where quadratic work is sixteen times. Every generator multiplies one
// thing an input can have many of — elements, attributes, values, errors of
// one kind, violations of one rule, files — with a variant that is valid
// and one that is not where both exist, so that every stage of a validation
// and every check sees many of what it handles. A new check, or a new kind
// of finding, belongs in the table below.
//
// Work is measured in two ways. The rules and the type checks count their
// work in XPath steps, which are the same on every machine and under any
// load: their steps for 4n may be at most stepFactor times those for n.
// The other stages — reading and parsing, the schema, the references of a
// package, the report — have no such count, so the suite measures the CPU
// time of the process, which other processes on a busy machine do not add
// to as they do to the time on the clock. It takes the least of some runs,
// at a size large enough to stand out from the noise, and a case that is
// over the limit is measured again: it fails only when every attempt is.
//
// Work that is not linear by design is bounded by the step budget of the
// rules instead; rules.TestBudgetGrowsWithDocument covers that. A
// generator of this table must not run out of steps.

// scalingCase is a generator of the suite.
type scalingCase struct {
	name string
	// doc or pkg makes the input for a size.
	doc func(n int) string
	pkg func(n int) []zipEntry
	// n is the smaller size; zero selects scalingSize.
	n int
	// want is what the validation must come to, so that a generator keeps
	// reaching the stage it is meant for: "valid", or the code of a finding
	// it must give.
	want string
	// most is the largest n the input can have, as for the files of a
	// package; zero sets none.
	most int
}

// maxN is the largest size the case is measured at.
func (c scalingCase) maxN() int {
	if c.most > 0 {
		return c.most
	}
	return 1 << 20
}

const scalingSize = 2000

type zipEntry struct{ name, data string }

func zipOf(t testing.TB, entries []zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// rep writes f(i) for i below n.
func rep(n int, f func(i int) string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString(f(i))
	}
	return b.String()
}

// each returns a generator of one unit, numbered where it has a %d.
func each(unit string) func(i int) string {
	if !strings.Contains(unit, "%d") {
		return func(int) string { return unit }
	}
	count := strings.Count(unit, "%d")
	return func(i int) string {
		args := make([]any, count)
		for k := range args {
			args[k] = i
		}
		return fmt.Sprintf(unit, args...)
	}
}

const (
	asiNS    = `xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"`
	itemHead = `<qti-assessment-item ` + asiNS + ` identifier="i" title="t" adaptive="false" time-dependent="false">`
	itemTail = `</qti-assessment-item>`
	testHead = `<qti-assessment-test ` + asiNS + ` identifier="T" title="t">`
	testTail = `</qti-assessment-test>`
	partHead = `<qti-test-part identifier="P" navigation-mode="linear" submission-mode="individual">`
	secHead  = `<qti-assessment-section identifier="S" title="s" visible="true">`
	score    = `<qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/>`
	response = `<qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/>`
	plainRP  = `<qti-response-processing><qti-set-outcome-value identifier="SCORE"><qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value></qti-response-processing>`
)

// item is an item of declarations, a body and what follows the body.
func item(decls, body, after string) string {
	return itemHead + decls + `<qti-item-body>` + body + `</qti-item-body>` + after + itemTail
}

// bodyOf is an item whose body is n units.
func bodyOf(unit string) func(n int) string {
	return func(n int) string { return item(response, rep(n, each(unit)), "") }
}

// rulesOf is an item with decls whose response processing is n units.
func rulesOf(decls, unit string) func(n int) string {
	return func(n int) string {
		return item(decls, `<p>x</p>`, `<qti-response-processing>`+rep(n, each(unit))+`</qti-response-processing>`)
	}
}

// test is a test of declarations, one part with one section of content, and
// what follows the parts.
func test(decls, section, after string) string {
	return testHead + decls + partHead + secHead + section + `</qti-assessment-section></qti-test-part>` + after + testTail
}

func setScore(expr string) string {
	return `<qti-set-outcome-value identifier="SCORE">` + expr + `</qti-set-outcome-value>`
}

func baseValue(base, value string) string {
	return `<qti-base-value base-type="` + base + `">` + value + `</qti-base-value>`
}

// valueSamples are a valid and an invalid value of every base type.
var valueSamples = map[string][2]string{
	"boolean": {"true", "yes"}, "integer": {"12", "1.5"}, "float": {"1.5", "x"}, "string": {"text", ""},
	"point": {"1 2", "1"}, "pair": {"A B", "A"}, "directedPair": {"A B", "A B C"}, "duration": {"1.5", "x"},
	"file": {"f", ""}, "uri": {"a/b", ""}, "identifier": {"A", "1 A"},
}

// operatorArity is the number of operands the scaling cases give an
// operator of the type checks: every one of them of the wrong base type or
// cardinality for some operator.
var scalingOperators = map[string]int{
	"qti-multiple": 2, "qti-ordered": 2, "qti-container-size": 1, "qti-is-null": 1, "qti-index": 1, "qti-random": 1,
	"qti-member": 2, "qti-delete": 2, "qti-contains": 2, "qti-substring": 2, "qti-repeat": 2,
	"qti-not": 1, "qti-and": 2, "qti-or": 2, "qti-any-n": 2, "qti-match": 2, "qti-string-match": 2,
	"qti-pattern-match": 1, "qti-equal": 2, "qti-equal-rounded": 2, "qti-lt": 2, "qti-gt": 2, "qti-lte": 2,
	"qti-gte": 2, "qti-duration-lt": 2, "qti-duration-gte": 2, "qti-inside": 1,
	"qti-sum": 2, "qti-product": 2, "qti-max": 2, "qti-min": 2, "qti-subtract": 2, "qti-divide": 2, "qti-power": 2,
	"qti-round-to": 1, "qti-math-operator": 1, "qti-truncate": 1, "qti-round": 1, "qti-integer-divide": 2,
	"qti-integer-modulus": 2, "qti-integer-to-float": 1, "qti-gcd": 2, "qti-lcm": 2, "qti-stats-operator": 1,
}

// operatorAttrs are the attributes the schema requires of an operator.
var operatorAttrs = map[string]string{
	"qti-index": ` n="1"`, "qti-substring": ` case-sensitive="true"`, "qti-repeat": ` number-repeats="2"`,
	"qti-any-n": ` min="1" max="2"`, "qti-string-match": ` case-sensitive="true"`, "qti-pattern-match": ` pattern="a"`,
	"qti-equal": ` tolerance-mode="exact"`, "qti-equal-rounded": ` figures="2"`, "qti-inside": ` shape="rect" coords="0,0,1,1"`,
	"qti-round-to": ` figures="2" rounding-mode="decimalPlaces"`, "qti-math-operator": ` name="sin"`, "qti-stats-operator": ` name="mean"`,
}

// scalingCases returns the table of the suite.
func scalingCases() []scalingCase {
	cases := []scalingCase{
		// ---- XML shapes
		{name: "xml/wide", doc: bodyOf(`<p>x</p>`), want: "valid"},
		{name: "xml/deep", want: "valid", doc: func(n int) string {
			return item(response, strings.Repeat("<div>", 200)+rep(n, each(`<p>x</p>`))+strings.Repeat("</div>", 200), "")
		}},
		{name: "xml/deep chains", want: "valid", doc: func(n int) string {
			return item(response, rep(n/50, each(strings.Repeat("<div>", 50)+`<p>x</p>`+strings.Repeat("</div>", 50))), "")
		}},
		{name: "xml/attributes", doc: bodyOf(`<p id="p%d" class="a b c" dir="ltr" data-a="1" data-b="2" data-c="3" data-d="4" aria-label="x">x</p>`), want: "valid"},
		// The schema library takes no more than some hundreds of attributes
		// on an element: many elements with many each.
		{name: "xml/elements of many attributes", n: 200, want: "valid", doc: bodyOf(`<p` + rep(100, each(` data-a%d="v"`)) + `>x</p>`)},
		{name: "xml/namespace declarations", doc: bodyOf(`<p xmlns:n%d="urn:n%d">x</p>`), want: "valid"},
		{name: "xml/elements of many namespace declarations", n: 200, want: "valid", doc: bodyOf(`<p` + rep(100, each(` xmlns:n%d="urn:n%d"`)) + `>x</p>`)},
		{name: "xml/cdata runs", n: 20000, doc: cdataItem, want: "valid"},
		{name: "xml/comments and processing instructions", doc: bodyOf(`<!-- c %d --><?pi d%d?><p>x</p>`), want: "valid"},
		{name: "xml/long names", doc: bodyOf(`<` + strings.Repeat("n", 60) + `%d/>`), want: string(qti.CodeValidation)},
		{name: "xml/long attribute values", doc: bodyOf(`<p class="` + strings.Repeat("v ", 100) + `">x</p>`), want: "valid"},
		{name: "xml/huge text", n: 20000, want: "valid", doc: func(n int) string {
			return item(response, `<p>`+strings.Repeat("word and another ", n)+`</p>`, "")
		}},
		{name: "xml/mixed content", doc: bodyOf(`<p>a <em>b</em> c <strong>d</strong> e &amp; f</p>`), want: "valid"},
		{name: "xml/entity references", n: 20000, want: "valid", doc: func(n int) string {
			return item(response, `<p>`+strings.Repeat("&amp;&#65;&lt;", n)+`</p>`, "")
		}},
		{name: "xml/unsupported root", want: string(qti.CodeUnsupportedDocument), doc: func(n int) string {
			return `<other xmlns="urn:other" xmlns:p="urn:p">` + rep(n, each(`<p:a p:x="%d"><b/></p:a>`)) + `</other>`
		}},
		{name: "xml/malformed at the end", want: string(qti.CodeInvalidXML), doc: func(n int) string {
			return item(response, rep(n, each(`<p>x</p>`))+`<p>`, "")
		}},

		// ---- XSD errors
		{name: "xsd/unknown elements in one parent", doc: bodyOf(`<bogus/>`), want: string(qti.CodeValidation)},
		{name: "xsd/unknown attributes", doc: bodyOf(`<p bogus="1">x</p>`), want: string(qti.CodeSchematron)}, // a rule of 1EdTech, not the schema
		{name: "xsd/invalid attribute values", doc: bodyOf(`<p dir="sideways">x</p>`), want: string(qti.CodeValidation)},
		{name: "xsd/missing required attributes", doc: bodyOf(`<qti-choice-interaction><qti-simple-choice>x</qti-simple-choice></qti-choice-interaction>`), want: string(qti.CodeValidation)},
		{name: "xsd/one error at the end", want: string(qti.CodeValidation), doc: func(n int) string {
			return item(response, rep(n, each(`<p>x</p>`))+`<bogus/>`, "")
		}},
		{name: "xsd/ids", doc: bodyOf(`<p id="id%d">x</p>`), want: "valid"},
		{name: "xsd/duplicate ids", doc: bodyOf(`<p id="same">x</p>`), want: string(qti.CodeValidation)},
		{name: "xsd/idrefs", doc: bodyOf(`<p id="id%d">x</p><table><tr><th id="h%d">h</th><td headers="h%d">c</td></tr></table>`), want: "valid"},
		{name: "xsd/headers that name no cell", want: "valid", doc: bodyOf(`<table><tr><th>h</th><td headers="none%d">c</td></tr></table>`)},

		// ---- the additional checks, with and without violations
		{name: "rules/response-declaration-exists/ok", doc: testutil.ItemWithTextEntries, want: "valid"},
		{name: "rules/response-declaration-exists/violations", doc: bodyOf(`<p><qti-text-entry-interaction response-identifier="NONE%d"/></p>`), want: string(qti.CodeValidation)}, // an IDREF of the schema
		{name: "rules/variable-declared/ok", want: "valid", doc: func(n int) string {
			return item(rep(n, each(`<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`)), `<p>x</p>`,
				`<qti-response-processing>`+rep(n, each(`<qti-set-outcome-value identifier="O%d"><qti-variable identifier="O%d"/></qti-set-outcome-value>`))+`</qti-response-processing>`)
		}},
		{name: "rules/variable-declared/violations", doc: rulesOf(score, setScore(`<qti-variable identifier="NONE%d"/>`)), want: string(qti.CodeSchematron)},
		{name: "rules/variable-declared/correct and map-response", doc: rulesOf(score, setScore(`<qti-sum><qti-map-response identifier="NONE%d"/><qti-map-response-point identifier="N%d"/></qti-sum>`)), want: string(qti.CodeSchematron)},
		{name: "rules/variable-declared/printed variables", doc: bodyOf(`<p><qti-printed-variable identifier="NONE%d"/></p>`), want: string(qti.CodeSchematron)},
		{name: "rules/set-target-declared/violations", doc: rulesOf(score, `<qti-set-outcome-value identifier="NONE%d"><qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value>`), want: string(qti.CodeSchematron)},
		{name: "rules/set-target-declared/ok", doc: rulesOf(score, setScore(baseValue("float", "1"))), want: "valid"},
		{name: "rules/show-hide-variable/ok", want: "valid", doc: func(n int) string {
			return item(response+`<qti-outcome-declaration identifier="FB" cardinality="single" base-type="identifier"/>`,
				rep(n, each(`<p><qti-feedback-inline outcome-identifier="FB" identifier="f%d">x</qti-feedback-inline></p>`)), "")
		}},
		{name: "rules/show-hide-variable/violations", doc: bodyOf(`<p><qti-feedback-inline outcome-identifier="NONE%d" identifier="f%d">x</qti-feedback-inline></p>`), want: string(qti.CodeSchematron)},
		{name: "rules/show-hide-variable/template identifiers", doc: bodyOf(`<qti-template-block template-identifier="NONE%d" identifier="t%d"><qti-content-body><p>x</p></qti-content-body></qti-template-block>`), want: string(qti.CodeSchematron)},
		{name: "rules/builtin-not-declared", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(rep(n, each(`<qti-outcome-declaration identifier="completionStatus" cardinality="single" base-type="identifier"/>`)), `<p>x</p>`, "")
		}},
		{name: "rules/identifier-unique/declarations ok", want: "valid", doc: func(n int) string {
			return item(rep(n, each(`<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`)), `<p>x</p>`, "")
		}},
		{name: "rules/identifier-unique/declarations all the same", want: string(qti.CodeValidation), doc: func(n int) string { // an ID of the schema
			return item(rep(n, each(`<qti-response-declaration identifier="R" cardinality="single" base-type="string"/>`)), `<p>x</p>`, "")
		}},
		{name: "rules/identifier-unique/choices ok", want: "valid", doc: func(n int) string {
			return item(response, `<qti-choice-interaction response-identifier="RESPONSE" max-choices="1">`+rep(n, each(`<qti-simple-choice identifier="c%d">x</qti-simple-choice>`))+`</qti-choice-interaction>`, "")
		}},
		{name: "rules/identifier-unique/choices all the same", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(response, `<qti-choice-interaction response-identifier="RESPONSE" max-choices="1">`+rep(n, each(`<qti-simple-choice identifier="same">x</qti-simple-choice>`))+`</qti-choice-interaction>`, "")
		}},
		{name: "rules/identifier-unique/choices in many interactions", want: "valid", doc: func(n int) string {
			return item(rep(n/4, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"/>`)),
				rep(n/4, each(`<qti-choice-interaction response-identifier="R%d" max-choices="1"><qti-simple-choice identifier="a">a</qti-simple-choice><qti-simple-choice identifier="b">b</qti-simple-choice><qti-simple-choice identifier="c">c</qti-simple-choice></qti-choice-interaction>`)), "")
		}},
		{name: "rules/test-identifier-unique/ok", want: "valid", doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"/>`)), "")
		}},
		{name: "rules/test-identifier-unique/all the same", want: string(qti.CodeSchematron), doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I" href="i%d.xml"/>`)), "")
		}},
		{name: "rules/branch-target-exists/ok", want: "valid", doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"><qti-branch-rule target="EXIT_SECTION">`+baseValue("boolean", "true")+`</qti-branch-rule></qti-assessment-item-ref>`)), "")
		}},
		{name: "rules/branch-target-exists/targets later on", want: "valid", doc: func(n int) string {
			return test("", rep(n, func(i int) string {
				target := "I" + strconv.Itoa(n-1)
				if i == n-1 {
					target = "EXIT_TEST" // the last has nothing after it
				}
				return `<qti-assessment-item-ref identifier="I` + strconv.Itoa(i) + `" href="i.xml"><qti-branch-rule target="` + target + `">` + baseValue("boolean", "true") + `</qti-branch-rule></qti-assessment-item-ref>`
			}), "")
		}},
		{name: "rules/branch-target-exists/targets earlier on", want: string(qti.CodeSchematron), doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i.xml"><qti-branch-rule target="I0">`+baseValue("boolean", "true")+`</qti-branch-rule></qti-assessment-item-ref>`)), "")
		}},
		{name: "rules/branch-target-exists/section refs", want: "valid", doc: sectionRefsAndBranchRules},
		{name: "rules/branch-target-exists/violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"><qti-branch-rule target="NONE%d">`+baseValue("boolean", "true")+`</qti-branch-rule></qti-assessment-item-ref>`)), "")
		}},
		{name: "rules/pre-conditions", want: "valid", doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"><qti-pre-condition>`+baseValue("boolean", "true")+`</qti-pre-condition></qti-assessment-item-ref>`)), "")
		}},
		{name: "rules/test-item-variable/ok", want: "valid", doc: func(n int) string {
			return test(score, rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"/>`)),
				`<qti-outcome-processing>`+setScore(`<qti-sum>`+rep(n, each(`<qti-variable identifier="I%d.SCORE"/>`))+`</qti-sum>`)+`</qti-outcome-processing>`)
		}},
		{name: "rules/test-item-variable/violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return test(score, rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i%d.xml"/>`)),
				`<qti-outcome-processing>`+setScore(`<qti-sum>`+rep(n, each(`<qti-variable identifier="X%d.SCORE"/>`))+`</qti-sum>`)+`</qti-outcome-processing>`)
		}},
		{name: "rules/variable-declared/test violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return test(score, `<qti-assessment-item-ref identifier="I" href="i.xml"/>`,
				`<qti-outcome-processing>`+setScore(`<qti-sum>`+rep(n, each(`<qti-variable identifier="NONE%d"/>`))+`</qti-sum>`)+`</qti-outcome-processing>`)
		}},
		{name: "rules/mapping-declared/violations", want: string(qti.CodeSchematron), doc: rulesOf(response+score, setScore(`<qti-map-response identifier="RESPONSE"/>`))},
		{name: "rules/mapping-declared/many declarations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(rep(n, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="point"/>`))+score, `<p>x</p>`,
				`<qti-response-processing>`+rep(n, each(setScore(`<qti-map-response-point identifier="R%d"/>`)))+`</qti-response-processing>`)
		}},
		{name: "rules/correct-response-declared/warnings", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(rep(n, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"/>`))+score, `<p>x</p>`,
				`<qti-response-processing>`+rep(n, each(`<qti-response-condition><qti-response-if><qti-match><qti-variable identifier="R%d"/><qti-correct identifier="R%d"/></qti-match>`+setScore(baseValue("float", "1"))+`</qti-response-if></qti-response-condition>`))+`</qti-response-processing>`)
		}},
		{name: "rules/correct-response-declared/ok", want: "valid", doc: func(n int) string {
			return item(rep(n, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"><qti-correct-response><qti-value>A</qti-value></qti-correct-response></qti-response-declaration>`))+score, `<p>x</p>`,
				`<qti-response-processing>`+rep(n, each(`<qti-response-condition><qti-response-if><qti-match><qti-variable identifier="R%d"/><qti-correct identifier="R%d"/></qti-match>`+setScore(baseValue("float", "1"))+`</qti-response-if></qti-response-condition>`))+`</qti-response-processing>`)
		}},
		{name: "rules/template-processing-scope/violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return itemHead + response + score + `<qti-template-declaration identifier="T" cardinality="single" base-type="float"/>` +
				`<qti-template-processing>` + rep(n, each(`<qti-set-template-value identifier="T"><qti-variable identifier="SCORE"/></qti-set-template-value>`)) + `</qti-template-processing>` +
				`<qti-item-body><p>x</p></qti-item-body>` + itemTail
		}},
		{name: "rules/template-processing-scope/ok", doc: func(n int) string {
			return itemHead + response + `<qti-template-declaration identifier="T" cardinality="single" base-type="float"/>` +
				`<qti-template-processing>` + rep(n, each(`<qti-set-template-value identifier="T"><qti-variable identifier="T"/></qti-set-template-value>`)) + `</qti-template-processing>` +
				`<qti-item-body><p>x</p></qti-item-body>` + itemTail
		}, want: "valid"},
		{name: "rules/interaction-response-type/violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(rep(n, each(`<qti-response-declaration identifier="R%d" cardinality="ordered" base-type="string"/>`)),
				rep(n, each(`<qti-choice-interaction response-identifier="R%d" max-choices="1"><qti-simple-choice identifier="a">a</qti-simple-choice></qti-choice-interaction>`)), "")
		}},
		{name: "rules/choice-value-exists/ok", want: "valid", doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="identifier"><qti-correct-response>`+rep(n, each(`<qti-value>c%d</qti-value>`))+`</qti-correct-response></qti-response-declaration>`,
				`<qti-choice-interaction response-identifier="RESPONSE" max-choices="0">`+rep(n, each(`<qti-simple-choice identifier="c%d">x</qti-simple-choice>`))+`</qti-choice-interaction>`, "")
		}},
		{name: "rules/choice-value-exists/violations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="identifier"><qti-correct-response>`+rep(n, each(`<qti-value>x%d</qti-value>`))+`</qti-correct-response><qti-mapping default-value="0">`+rep(n, each(`<qti-map-entry map-key="y%d" mapped-value="1"/>`))+`</qti-mapping></qti-response-declaration>`,
				`<qti-choice-interaction response-identifier="RESPONSE" max-choices="0">`+rep(n, each(`<qti-simple-choice identifier="c%d">x</qti-simple-choice>`))+`</qti-choice-interaction>`, "")
		}},
		{name: "rules/choice-value-exists/pairs", want: "valid", doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="directedPair"><qti-correct-response>`+rep(n, each(`<qti-value>a%d b%d</qti-value>`))+`</qti-correct-response></qti-response-declaration>`,
				`<qti-match-interaction response-identifier="RESPONSE" max-associations="0"><qti-simple-match-set>`+rep(n, each(`<qti-simple-associable-choice identifier="a%d" match-max="1">x</qti-simple-associable-choice>`))+
					`</qti-simple-match-set><qti-simple-match-set>`+rep(n, each(`<qti-simple-associable-choice identifier="b%d" match-max="1">x</qti-simple-associable-choice>`))+`</qti-simple-match-set></qti-match-interaction>`, "")
		}},
		{name: "rules/variable-used/warnings", want: string(qti.CodeSchematron), doc: func(n int) string {
			return item(score+rep(n, each(`<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`)), `<p>x</p>`, plainRP)
		}},
		{name: "rules/variable-used/ok", want: "valid", doc: func(n int) string {
			return item(rep(n, each(`<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`)), `<p>x</p>`,
				`<qti-response-processing>`+rep(n, each(`<qti-set-outcome-value identifier="O%d">`+baseValue("float", "1")+`</qti-set-outcome-value>`))+`</qti-response-processing>`)
		}},
		{name: "rules/variable-used/template declarations", want: string(qti.CodeSchematron), doc: func(n int) string {
			return itemHead + response + rep(n, each(`<qti-template-declaration identifier="T%d" cardinality="single" base-type="float"/>`)) +
				`<qti-template-processing><qti-set-template-value identifier="T0">` + baseValue("float", "1") + `</qti-set-template-value></qti-template-processing>` +
				`<qti-item-body><p>x</p></qti-item-body>` + itemTail
		}},

		// ---- shapes the 1EdTech rules look at
		{name: "1edtech/declarations and interactions", doc: testutil.ItemWithDeclarations, n: 1000},
		{name: "1edtech/nested interactions", doc: func(n int) string { return testutil.NestedInteractions(200, 25*n) }},
		{name: "1edtech/inline choices", want: "valid", doc: func(n int) string {
			return item(rep(n/4, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"/>`)),
				rep(n/4, each(`<p><qti-inline-choice-interaction response-identifier="R%d"><qti-inline-choice identifier="a">a</qti-inline-choice><qti-inline-choice identifier="b">b</qti-inline-choice></qti-inline-choice-interaction></p>`)), "")
		}},
		{name: "1edtech/hottexts", want: "valid", doc: func(n int) string {
			return item(response, `<qti-hottext-interaction response-identifier="RESPONSE" max-choices="1"><p>`+rep(n, each(`a <qti-hottext identifier="h%d">b</qti-hottext> `))+`</p></qti-hottext-interaction>`, "")
		}},
		{name: "1edtech/gap match", doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="directedPair"/>`,
				`<qti-gap-match-interaction response-identifier="RESPONSE" max-associations="0">`+rep(n, each(`<qti-gap-text identifier="t%d" match-max="1">x</qti-gap-text>`))+
					`<p>`+rep(n, each(`a <qti-gap identifier="g%d"/> `))+`</p></qti-gap-match-interaction>`, "")
		}},
		{name: "1edtech/order interaction", doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="ordered" base-type="identifier"/>`,
				`<qti-order-interaction response-identifier="RESPONSE">`+rep(n, each(`<qti-simple-choice identifier="c%d">x</qti-simple-choice>`))+`</qti-order-interaction>`, "")
		}},
		{name: "1edtech/extended text interactions", want: "valid", doc: func(n int) string {
			return item(rep(n, each(`<qti-response-declaration identifier="R%d" cardinality="single" base-type="string"/>`)),
				rep(n, each(`<qti-extended-text-interaction response-identifier="R%d"/>`)), "")
		}},
		{name: "1edtech/choice interactions with min and max", doc: func(n int) string {
			return item(rep(n/4, each(`<qti-response-declaration identifier="R%d" cardinality="multiple" base-type="identifier"/>`)),
				rep(n/4, each(`<qti-choice-interaction response-identifier="R%d" max-choices="1" min-choices="2"><qti-simple-choice identifier="a">a</qti-simple-choice><qti-simple-choice identifier="b">b</qti-simple-choice></qti-choice-interaction>`)), "")
		}},
		{name: "1edtech/response conditions", want: "valid", doc: rulesOf(response+score,
			`<qti-response-condition><qti-response-if><qti-match><qti-variable identifier="RESPONSE"/>`+baseValue("identifier", "A")+`</qti-match>`+setScore(baseValue("float", "1"))+
				`</qti-response-if><qti-response-else>`+setScore(baseValue("float", "0"))+`</qti-response-else></qti-response-condition>`)},
		{name: "1edtech/nested response conditions", want: "valid", doc: func(n int) string {
			const open = `<qti-response-condition><qti-response-if><qti-is-null><qti-variable identifier="RESPONSE"/></qti-is-null>`
			return item(response+score, `<p>x</p>`, `<qti-response-processing>`+rep(n/50, each(strings.Repeat(open, 50)+strings.Repeat(`</qti-response-if></qti-response-condition>`, 50)))+setScore(baseValue("float", "1"))+`</qti-response-processing>`)
		}},
		{name: "1edtech/mappings", want: "valid", doc: func(n int) string {
			return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="string"><qti-mapping default-value="0">`+rep(n, each(`<qti-map-entry map-key="k%d" mapped-value="1"/>`))+`</qti-mapping></qti-response-declaration>`, `<p>x</p>`, "")
		}},
		{name: "1edtech/sections and item refs", want: "valid", doc: func(n int) string {
			return testHead + partHead + rep(n/10, func(i int) string {
				return `<qti-assessment-section identifier="S` + strconv.Itoa(i) + `" title="s" visible="true">` + rep(10, func(k int) string {
					return `<qti-assessment-item-ref identifier="I` + strconv.Itoa(i) + `_` + strconv.Itoa(k) + `" href="i.xml"/>`
				}) + `</qti-assessment-section>`
			}) + `</qti-test-part>` + testTail
		}},
		{name: "1edtech/test parts", want: "valid", doc: func(n int) string {
			return testHead + rep(n, each(`<qti-test-part identifier="P%d" navigation-mode="linear" submission-mode="individual"><qti-assessment-section identifier="S%d" title="s" visible="true"><qti-assessment-item-ref identifier="I%d" href="i.xml"/></qti-assessment-section></qti-test-part>`)) + testTail
		}},
		{name: "1edtech/nested sections", want: "valid", doc: func(n int) string {
			return testHead + partHead + rep(n/50, func(i int) string {
				return rep(50, func(k int) string {
					return `<qti-assessment-section identifier="S` + strconv.Itoa(i) + `_` + strconv.Itoa(k) + `" title="s" visible="true">`
				}) + `<qti-assessment-item-ref identifier="I` + strconv.Itoa(i) + `" href="i.xml"/>` + strings.Repeat(`</qti-assessment-section>`, 50)
			}) + `</qti-test-part>` + testTail
		}},
		{name: "1edtech/item refs with weights and variable mappings", doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i.xml" category="a b"><qti-variable-mapping source-identifier="SCORE" target-identifier="S%d"/><qti-weight identifier="W" value="1"/></qti-assessment-item-ref>`)), "")
		}},
		{name: "1edtech/stimulus refs", doc: func(n int) string {
			return itemHead + response + rep(n, each(`<qti-assessment-stimulus-ref identifier="s%d" href="s%d.xml"/>`)) + `<qti-item-body><p>x</p></qti-item-body>` + itemTail
		}},
		{name: "1edtech/images and objects", doc: bodyOf(`<p><img src="a%d.png" alt="x"/><object data="o%d.svg" type="image/svg+xml">x</object></p>`), want: "valid"},

		// ---- the report
		{name: "report/package of invalid documents", most: 900, n: 50, pkg: func(n int) []zipEntry {
			return packageOf(n, func(int) string {
				return item(response, rep(120, each(`<p><qti-text-entry-interaction response-identifier="NONE%d"/></p>`)), "")
			})
		}},
	}

	// ---- the type checks: every base type, valid and not
	for base, sample := range valueSamples {
		for k, kind := range []string{"valid", "invalid"} {
			value := sample[k]
			cases = append(cases,
				scalingCase{name: "types/values/" + base + "/" + kind, doc: func(n int) string {
					return item(`<qti-outcome-declaration identifier="O" cardinality="multiple" base-type="`+base+`"><qti-default-value>`+
						strings.Repeat(`<qti-value>`+value+`</qti-value>`, n)+`</qti-default-value></qti-outcome-declaration>`, `<p>x</p>`, "")
				}},
				scalingCase{name: "types/base-values/" + base + "/" + kind, doc: rulesOf(
					`<qti-outcome-declaration identifier="O" cardinality="single" base-type="`+base+`"/>`,
					`<qti-set-outcome-value identifier="O">`+baseValue(base, value)+`</qti-set-outcome-value>`)},
				scalingCase{name: "types/map keys/" + base + "/" + kind, doc: func(n int) string {
					return item(`<qti-response-declaration identifier="RESPONSE" cardinality="multiple" base-type="`+base+`"><qti-mapping default-value="0">`+
						strings.Repeat(`<qti-map-entry map-key="`+value+`" mapped-value="1"/>`, n)+`</qti-mapping></qti-response-declaration>`, `<p>x</p>`, "")
				}})
		}
	}
	cases = append(cases,
		scalingCase{name: "types/record fields", doc: func(n int) string {
			return item(`<qti-outcome-declaration identifier="O" cardinality="record"><qti-default-value>`+
				rep(n, each(`<qti-value field-identifier="f%d" base-type="integer">x</qti-value>`))+`</qti-default-value></qti-outcome-declaration>`, `<p>x</p>`, "")
		}},
		scalingCase{
			name: "types/assignments of another cardinality", want: string(qti.CodeValueType),
			doc: rulesOf(score, setScore(`<qti-multiple>`+baseValue("float", "1")+`</qti-multiple>`)),
		},
		scalingCase{
			name: "types/assignments of another base type", want: string(qti.CodeValueType),
			doc: rulesOf(score, setScore(baseValue("string", "x"))),
		},
		scalingCase{
			name: "types/conditions that are not boolean", want: string(qti.CodeValueType),
			doc: rulesOf(score, `<qti-response-condition><qti-response-if>`+baseValue("integer", "1")+setScore(baseValue("float", "1"))+`</qti-response-if></qti-response-condition>`),
		},
		scalingCase{name: "types/branch rules that are not boolean", want: string(qti.CodeValueType), doc: func(n int) string {
			return test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="i.xml"><qti-branch-rule target="EXIT_TEST">`+baseValue("integer", "1")+`</qti-branch-rule></qti-assessment-item-ref>`)), "")
		}},
		scalingCase{name: "types/many operands of one operator", want: string(qti.CodeValueType), doc: func(n int) string {
			return item(score, `<p>x</p>`, `<qti-response-processing>`+setScore(`<qti-sum>`+strings.Repeat(baseValue("string", "x"), n)+`</qti-sum>`)+`</qti-response-processing>`)
		}},
		scalingCase{name: "types/many operands of the same type", want: string(qti.CodeValueType), doc: func(n int) string {
			return item(score, `<p>x</p>`, `<qti-response-processing>`+setScore(`<qti-container-size><qti-multiple>`+baseValue("integer", "1")+strings.Repeat(baseValue("string", "x"), n)+`</qti-multiple></qti-container-size>`)+`</qti-response-processing>`)
		}},
		scalingCase{name: "types/deep expressions", doc: func(n int) string {
			return item(score, `<p>x</p>`, `<qti-response-processing>`+rep(n/50, each(setScore(strings.Repeat(`<qti-sum>`, 50)+baseValue("string", "x")+strings.Repeat(`</qti-sum>`, 50))))+`</qti-response-processing>`)
		}},
	)
	for op, arity := range scalingOperators {
		operands := strings.Repeat(baseValue("identifier", "A"), arity)
		if arity == 2 {
			operands = baseValue("identifier", "A") + `<qti-multiple>` + baseValue("point", "1 2") + `</qti-multiple>`
		}
		cases = append(cases, scalingCase{name: "types/operators/" + op, n: 1000, doc: rulesOf(
			`<qti-outcome-declaration identifier="O" cardinality="single" base-type="duration"/>`,
			`<qti-set-outcome-value identifier="O"><`+op+operatorAttrs[op]+`>`+operands+`</`+op+`></qti-set-outcome-value>`)})
	}

	// ---- packages
	cases = append(cases,
		scalingCase{name: "package/files", most: 900, n: 200, want: "valid", pkg: func(n int) []zipEntry {
			return packageOf(n, func(int) string { return item(response, `<p>x</p>`, "") })
		}},
		scalingCase{name: "package/files that are not listed", most: 900, n: 200, pkg: func(n int) []zipEntry {
			entries := packageOf(1, func(int) string { return item(response, `<p>x</p>`, "") })
			for i := range n {
				entries = append(entries, zipEntry{fmt.Sprintf("extra/e%d.xml", i), item(response, `<p>x</p>`, "")})
			}
			return entries
		}},
		scalingCase{name: "package/resources of one file", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(rep(n, each(`<resource identifier="r%d" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`)), "")},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
		}},
		scalingCase{name: "package/missing files", pkg: func(n int) []zipEntry {
			return []zipEntry{{"imsmanifest.xml", manifestOf(rep(n, each(`<resource identifier="r%d" type="imsqti_item_xmlv3p0" href="none%d.xml"><file href="none%d.xml"/><file href="img%d.png"/></resource>`)), "")}}
		}},
		scalingCase{name: "package/dependencies", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/>`+rep(n, each(`<dependency identifierref="d%d"/>`))+`</resource>`+
					rep(n, each(`<resource identifier="d%d" type="webcontent" href="item.xml"><file href="item.xml"/><dependency identifierref="r"/></resource>`)), "")},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
		}},
		scalingCase{name: "package/unresolved dependencies", want: string(qti.CodeValidation), pkg: func(n int) []zipEntry { // an IDREF of the schema
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/>`+rep(n, each(`<dependency identifierref="none%d"/>`))+`</resource>`, "")},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
		}},
		scalingCase{name: "package/manifest metadata", pkg: func(n int) []zipEntry {
			lom := `<lom xmlns="http://ltsc.ieee.org/xsd/LOM"><general>` + rep(n, each(`<keyword><string language="en">k%d</string></keyword>`)) + `</general></lom>`
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><metadata>`+lom+`</metadata><file href="item.xml"/></resource>`, lom)},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
		}},
		scalingCase{name: "package/item refs of a test", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="t" type="imsqti_test_xmlv3p0" href="test.xml"><file href="test.xml"/><dependency identifierref="r"/></resource>`+
					`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`, "")},
				{"test.xml", test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="item.xml"/>`)), "")},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
		}},
		scalingCase{name: "package/item refs to missing files", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="t" type="imsqti_test_xmlv3p0" href="test.xml"><file href="test.xml"/></resource>`, "")},
				{"test.xml", test("", rep(n, each(`<qti-assessment-item-ref identifier="I%d" href="none%d.xml"/>`)), "")},
			}
		}},
		scalingCase{name: "package/tests and sections", most: 900, n: 100, pkg: func(n int) []zipEntry {
			entries := []zipEntry{
				{"imsmanifest.xml", manifestOf(rep(n, each(`<resource identifier="t%d" type="imsqti_test_xmlv3p0" href="t%d.xml"><file href="t%d.xml"/><dependency identifierref="r"/></resource>`))+
					`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`, "")},
				{"item.xml", item(response, `<p>x</p>`, "")},
			}
			for i := range n {
				entries = append(entries, zipEntry{fmt.Sprintf("t%d.xml", i), test("", rep(20, each(`<qti-assessment-item-ref identifier="I%d" href="item.xml"/>`)), "")})
			}
			return entries
		}},
		scalingCase{name: "package/links to missing assets", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`, "")},
				{"item.xml", item(response, rep(n, each(`<p><img src="img/none%d.png" alt="x"/><a href="doc%d.html">x</a></p>`)), "")},
			}
		}},
		scalingCase{name: "package/includes of one fragment", pkg: func(n int) []zipEntry {
			return []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/><file href="frag.xml"/></resource>`, "")},
				{"item.xml", item(response, rep(n, each(`<div><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="frag.xml"/></div>`)), "")},
				{"frag.xml", `<div ` + asiNS + `><p>shared</p></div>`},
			}
		}},
		scalingCase{name: "package/fragments", most: 900, n: 100, pkg: func(n int) []zipEntry {
			entries := []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`+
					rep(n, each(`<resource identifier="f%d" type="imsqti_fragment_xmlv3p0" href="f%d.xml"><file href="f%d.xml"/></resource>`)), "")},
				{"item.xml", item(response, rep(n, each(`<div><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="f%d.xml"/></div>`)), "")},
			}
			for i := range n {
				entries = append(entries, zipEntry{fmt.Sprintf("f%d.xml", i), `<div ` + asiNS + `><p>shared</p><bogus/></div>`})
			}
			return entries
		}},
		scalingCase{name: "package/fragments that include each other", most: 900, n: 100, pkg: func(n int) []zipEntry {
			entries := []zipEntry{
				{"imsmanifest.xml", manifestOf(`<resource identifier="r" type="imsqti_item_xmlv3p0" href="item.xml"><file href="item.xml"/></resource>`, "")},
				{"item.xml", item(response, `<div><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="f0.xml"/></div>`, "")},
			}
			for i := range n {
				entries = append(entries, zipEntry{fmt.Sprintf("f%d.xml", i), fmt.Sprintf(`<div `+asiNS+`><p>shared</p><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="f%d.xml"/></div>`, (i+1)%n)})
			}
			return entries
		}},
	)
	return cases
}

// sectionRefsAndBranchRules is a valid test of one section with n section
// references and n item references, each of the latter with a branch rule
// to the last item. The rule asks, for every branch rule, whether its test
// part has a section reference at all.
func sectionRefsAndBranchRules(n int) string {
	return test("", rep(n, each(`<qti-assessment-section-ref identifier="R%d" href="s%d.xml"/>`))+rep(n, func(i int) string {
		target := "I" + strconv.Itoa(n-1)
		if i == n-1 {
			target = "EXIT_TEST"
		}
		return `<qti-assessment-item-ref identifier="I` + strconv.Itoa(i) + `" href="i.xml"><qti-branch-rule target="` + target + `">` + baseValue("boolean", "true") + `</qti-branch-rule></qti-assessment-item-ref>`
	}), "")
}

// A test with as many section references as branch rules is valid: asking
// for every rule whether the part has a section reference used to select
// them all each time, 20,000 times 20,000 steps, which ran out of the
// budget of the rules and gave a valid test the finding too_complex.
func TestSectionRefsAndBranchRulesStayWithinBudget(t *testing.T) {
	v, err := NewValidator(Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 20_000
	if raceEnabled {
		n = 5000
	}
	doc := sectionRefsAndBranchRules(n)
	before := rules.StepsCharged()
	res := v.ValidateDocument(t.Context(), app.ValidateDocument{Document: strings.NewReader(doc), Size: int64(len(doc))})
	steps := rules.StepsCharged() - before
	t.Logf("%d section references and branch rules, %d bytes: %d steps", n, len(doc), steps)
	if !res.Valid || len(res.Errors) != 0 {
		t.Fatalf("got %+v", res.Errors)
	}
	if steps > int64(200*n) {
		t.Errorf("%d steps for %d branch rules, want some tens each", steps, n)
	}
}

// manifestOf is a manifest with resources and metadata.
func manifestOf(resources, metadata string) string {
	return `<manifest xmlns="http://www.imsglobal.org/xsd/qti/qtiv3p0/imscp_v1p1" identifier="m"><metadata><schema>QTI Package</schema><schemaversion>3.0.0</schemaversion>` +
		metadata + `</metadata><organizations/><resources>` + resources + `</resources></manifest>`
}

// packageOf is a package of n items.
func packageOf(n int, doc func(i int) string) []zipEntry {
	entries := []zipEntry{{"imsmanifest.xml", manifestOf(rep(n, each(`<resource identifier="r%d" type="imsqti_item_xmlv3p0" href="items/i%d.xml"><file href="items/i%d.xml"/></resource>`)), "")}}
	for i := range n {
		entries = append(entries, zipEntry{fmt.Sprintf("items/i%d.xml", i), doc(i)})
	}
	return entries
}

// outcome is what a validation came to: valid without warnings, or the
// codes of its findings.
type outcome struct {
	valid            bool
	errors, warnings int
	codes            []string
	first            string // the message of the first finding
}

func outcomeOf(valid bool, errors, warnings []qti.Finding) outcome {
	o := outcome{valid: valid && len(warnings) == 0, errors: len(errors), warnings: len(warnings)}
	for _, f := range append(append([]qti.Finding{}, errors...), warnings...) {
		if !slices.Contains(o.codes, string(f.Code)) {
			o.codes = append(o.codes, string(f.Code))
		}
		if o.first == "" {
			o.first = f.Message
		}
	}
	return o
}

// is reports whether the outcome is what a case wants: "valid", or a code.
func (o outcome) is(want string) bool {
	if want == "valid" {
		return o.valid
	}
	return slices.Contains(o.codes, want)
}

func (o outcome) String() string {
	if o.valid {
		return "valid"
	}
	first := o.first
	if len(first) > 70 {
		first = first[:70] + "…"
	}
	return fmt.Sprintf("%d errors, %d warnings (%s): %s", o.errors, o.warnings, strings.Join(o.codes, ", "), first)
}

// What the suite holds the larger input to, in units of the smaller.
const (
	// Steps are exact. Sorting what a lookup or a pattern selected costs
	// n·log n of them, which is some 4.8 times for four times the nodes.
	stepFactor = 5
	// CPU time: linear work measures between three and five times, work
	// with a quadratic part that matters, seven and more.
	timeFactor = 7
	// The CPU time the smaller input must take before the larger is
	// measured, so that every case stands out from the noise.
	timeFloor = 6 * time.Millisecond
	// How often a case over the limit is measured again.
	timeAttempts = 5
)

// measurement is what one size of a case took.
type measurement struct {
	n     int
	bytes int
	cpu   time.Duration // the least of the runs
	steps int64         // of the rules and the type checks, the same every run
	came  outcome
}

func TestValidationScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("the scaling suite takes more than a minute")
	}
	v, err := NewValidator(Options{})
	if err != nil {
		t.Fatal(err)
	}
	// The race detector makes everything slow, not quadratic: smaller
	// inputs, once, exercise the code under it.
	runs, shrink := 2, 1
	if raceEnabled {
		runs, shrink = 1, 16
	}
	only := regexp.MustCompile(os.Getenv("SCALING")) // SCALING=^rules/ runs a part of the table
	for _, tc := range scalingCases() {
		if !only.MatchString(tc.name) {
			continue
		}
		measure := func(n, runs int) measurement {
			var data []byte
			if tc.pkg != nil {
				data = zipOf(t, tc.pkg(n))
			} else {
				data = []byte(tc.doc(n))
			}
			m := measurement{n: n, bytes: len(data), cpu: time.Duration(1<<63 - 1)}
			for range runs {
				runtime.GC() // what the last run left is not this run's work
				steps, start := rules.StepsCharged(), cpuTime()
				if tc.pkg != nil {
					res := v.ValidatePackage(t.Context(), app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
					_ = report.ForPackage(res, report.Meta{})
					var errs, warnings []qti.Finding
					for _, f := range res.Files {
						errs, warnings = append(errs, f.Errors...), append(warnings, f.Warnings...)
					}
					m.came = outcomeOf(res.Valid, append(errs, res.Errors...), warnings)
				} else {
					res := v.ValidateDocument(t.Context(), app.ValidateDocument{Document: bytes.NewReader(data), Size: int64(len(data))})
					_ = report.ForDocument(res, report.Meta{})
					m.came = outcomeOf(res.Valid, res.Errors, res.Warnings)
				}
				m.cpu = min(m.cpu, cpuTime()-start)
				m.steps = rules.StepsCharged() - steps
			}
			return m
		}
		n := tc.n
		if n == 0 {
			n = scalingSize
		}
		n = max(n/shrink, 8)
		// Grow the input until the smaller size stands out from the noise.
		small := measure(n, runs)
		for small.cpu < timeFloor && n < tc.maxN()/2 && small.bytes < 1<<20 { // four times a megabyte is well within the limits on size
			n *= 2
			small = measure(n, runs)
		}
		large := measure(4*n, runs)
		ratio := func() float64 { return float64(large.cpu) / float64(max(small.cpu, 1)) }
		within := func() bool { return large.cpu <= timeFactor*small.cpu+10*time.Millisecond }
		attempts := 1
		for ; !within() && attempts < timeAttempts; attempts++ {
			// Over the limit: noise, or work that is not linear. Noise does
			// not come back every time.
			small, large = measure(n, runs+1), measure(4*n, runs+1)
		}
		stepRatio := 0.0
		if small.steps > 0 {
			stepRatio = float64(large.steps) / float64(small.steps)
		}
		t.Logf("%-58s n=%-6d %8d B  steps %9d ×%4.1f  cpu %9v %9v ×%4.1f  %s", tc.name, n, small.bytes,
			small.steps, stepRatio, small.cpu.Round(10*time.Microsecond), large.cpu.Round(10*time.Microsecond), ratio(), small.came)
		if tc.want != "" && !small.came.is(tc.want) {
			t.Errorf("%s: came to %s, want %s", tc.name, small.came, tc.want)
		}
		// The larger input must be worked on as the smaller is, not turned
		// away early for its size.
		if !slices.Equal(small.came.codes, large.came.codes) {
			t.Errorf("%s: came to %s at n=%d, and to %s at n=%d", tc.name, small.came, n, large.came, 4*n)
		}
		if large.came.is(string(qti.CodeTooComplex)) || large.came.is(string(qti.CodeInternal)) {
			t.Errorf("%s: at n=%d came to %s", tc.name, 4*n, large.came)
		}
		if float64(large.steps) > stepFactor*float64(small.steps)+1000 {
			t.Errorf("%s: n=%d took %d steps and n=%d took %d (×%.1f): not linear", tc.name, n, small.steps, 4*n, large.steps, stepRatio)
		}
		if !within() {
			t.Errorf("%s: n=%d took %v of CPU time and n=%d took %v (×%.1f), in each of %d attempts: not linear",
				tc.name, n, small.cpu, 4*n, large.cpu, ratio(), attempts)
		}
	}
}
