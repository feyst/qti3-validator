package bootstrap

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf16"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xpath"
)

// The start and the end of an item around the content of its body.
const (
	bodyOpen  = itemOpen + `<qti-item-body>`
	bodyClose = `</qti-item-body></qti-assessment-item>`
)

// utf16Document is the document in UTF-16, which the validator refuses.

func utf16Document(s []byte) []byte {
	u := utf16.Encode([]rune(strings.Replace(string(s), "<qti-assessment-item", `<?xml version="1.0" encoding="UTF-16"?><qti-assessment-item`, 1)))
	out := make([]byte, 2, 2+2*len(u))
	out[0], out[1] = 0xFF, 0xFE
	for _, c := range u {
		out = binary.LittleEndian.AppendUint16(out, c)
	}
	return out
}

var adversarialShapes = []struct {
	name string
	doc  func(size int) []byte
}{
	{"ns-decl-unique-prefix", func(size int) []byte {
		return numberedTo(size, bodyOpen, bodyClose, func(b *bytes.Buffer, i int) { fmt.Fprintf(b, `<p xmlns:a%x="u"/>`, i) })
	}},
	{"ns-decl-same-prefix", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p xmlns:a="u"/>`, bodyClose)
	}},
	{"ns-decl-many-per-el", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p xmlns:a="" xmlns:b="" xmlns:c="" xmlns:d="" xmlns:e="" xmlns:f=""/>`, bodyClose)
	}},
	{"default-ns-redeclared", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>`, bodyClose)
	}},
	{"ns-short-default", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p xmlns=""/>`, bodyClose)
	}},
	{"unique-element-names", func(size int) []byte {
		return numberedTo(size, bodyOpen, bodyClose, func(b *bytes.Buffer, i int) { fmt.Fprintf(b, `<x%x/>`, i) })
	}},
	{"unique-names-in-p", func(size int) []byte {
		return numberedTo(size, bodyOpen+`<p>`, `</p>`+bodyClose, func(b *bytes.Buffer, i int) { fmt.Fprintf(b, `<x%x/>`, i) })
	}},
	{"whitespace-between", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p/> `, bodyClose)
	}},
	{"cdata-empty-runs", func(size int) []byte {
		return repeatTo(size, bodyOpen+`<p>`, `<![CDATA[]]><b/>`, `</p>`+bodyClose)
	}},
	{"cdata-then-comment", func(size int) []byte {
		return repeatTo(size, bodyOpen+`<p>`, `<![CDATA[]]><!---->`, `</p>`+bodyClose)
	}},
	{"char-refs", func(size int) []byte {
		return repeatTo(size, bodyOpen+`<p>`, `&#9;`, `</p>`+bodyClose)
	}},
	{"pis", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<?a?>`, bodyClose)
	}},
	{"pi-text-pi", func(size int) []byte {
		return repeatTo(size, bodyOpen+`<p>`, `<?a?>x`, `</p>`+bodyClose)
	}},
	{"xml-lang-attrs", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p xml:lang=""/>`, bodyClose)
	}},
	{"empty-attr-values", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p id="" class="" dir="" data-a="" data-b="" data-c=""/>`, bodyClose)
	}},
	{"attr-entity-values", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p class="&#9;&#9;&#9;&#9;"/>`, bodyClose)
	}},
	{"single-quotes-gt", func(size int) []byte {
		return repeatTo(size, bodyOpen, `<p class='>'/>`, bodyClose)
	}},
	{"deep-256-chains", func(size int) []byte {
		unit := strings.Repeat(`<b>`, 240) + strings.Repeat(`</b>`, 240)
		return repeatTo(size, bodyOpen+`<p>`, unit, `</p>`+bodyClose)
	}},
	{"response-decls", func(size int) []byte {
		return numberedTo(size, itemOpen, `<qti-item-body/></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-response-declaration identifier="R%x" cardinality="single" base-type="identifier"/>`, i)
		})
	}},
	{"response-decl-short", func(size int) []byte {
		return repeatTo(size, itemOpen, `<qti-response-declaration/>`, `<qti-item-body/></qti-assessment-item>`)
	}},
	{"interactions", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-item-body>`, bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-choice-interaction response-identifier="R%x"/>`, i)
		})
	}},
	{"text-entry", func(size int) []byte {
		return repeatTo(size, bodyOpen+`<p>`, `<qti-text-entry-interaction response-identifier="R"/>`, `</p>`+bodyClose)
	}},
	{"gaps", func(size int) []byte {
		return numberedTo(size, bodyOpen+`<qti-gap-match-interaction response-identifier="R"><p>`, `</p></qti-gap-match-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-gap identifier="g%x"/>`, i)
		})
	}},
	{"hottext", func(size int) []byte {
		return numberedTo(size, bodyOpen+`<qti-hottext-interaction response-identifier="R"><p>`, `</p></qti-hottext-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-hottext identifier="h%x"/>`, i)
		})
	}},

	{"hottext-declared", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="multiple" base-type="identifier"/>`+`<qti-item-body><qti-hottext-interaction response-identifier="R"><p>`, `</p></qti-hottext-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-hottext identifier="h%x"/>`, i)
		})
	}},
	{"hottext-many-interactions", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="multiple" base-type="identifier"/>`+`<qti-item-body>`, bodyClose, func(b *bytes.Buffer, _ int) {
			fmt.Fprintf(b, `<qti-hottext-interaction response-identifier="R"><p><qti-hottext identifier="h"/></p></qti-hottext-interaction>`)
		})
	}},
	{"inline-choices", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="single" base-type="identifier"/>`+`<qti-item-body><p><qti-inline-choice-interaction response-identifier="R">`, `</qti-inline-choice-interaction></p>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-inline-choice identifier="c%x"/>`, i)
		})
	}},
	{"gap-texts", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="multiple" base-type="directedPair"/>`+`<qti-item-body><qti-gap-match-interaction response-identifier="R">`, `<p><qti-gap identifier="g"/></p></qti-gap-match-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-gap-text identifier="t%x" match-max="1"/>`, i)
		})
	}},
	{"hottext-dup-ids", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="multiple" base-type="identifier"/>`+`<qti-item-body><qti-hottext-interaction response-identifier="R"><p>`, `<qti-hottext identifier="h"/>`, `</p></qti-hottext-interaction>`+bodyClose)
	}},
	{"choices-empty", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="single" base-type="identifier"/>`+`<qti-item-body><qti-choice-interaction response-identifier="R">`, `</qti-choice-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-simple-choice identifier="%x"/>`, i)
		})
	}},
	{"variables-in-rp", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="single" base-type="identifier"/><qti-item-body/><qti-response-processing><qti-response-condition><qti-response-if><qti-and>`, `</qti-and></qti-response-if></qti-response-condition></qti-response-processing></qti-assessment-item>`, func(b *bytes.Buffer, _ int) {
			fmt.Fprintf(b, `<qti-is-null><qti-variable identifier="R"/></qti-is-null>`)
		})
	}},

	{"decl-plus-entry", func(size int) []byte {
		n := size / 150
		var b bytes.Buffer
		b.WriteString(itemOpen)
		for i := range n {
			fmt.Fprintf(&b, `<qti-response-declaration identifier="R%x" cardinality="single" base-type="string"/>`, i)
		}
		b.WriteString(`<qti-item-body><p>`)
		for i := range n {
			fmt.Fprintf(&b, `<qti-text-entry-interaction response-identifier="R%x"/>`, i)
		}
		b.WriteString(`</p>` + bodyClose)
		return b.Bytes()
	}},
	{"hottext-with-child", func(size int) []byte {
		head := itemOpen + `<qti-response-declaration identifier="R" cardinality="multiple" base-type="identifier"/><qti-item-body><qti-hottext-interaction response-identifier="R"><p>`
		return numberedTo(size, head, `</p></qti-hottext-interaction>`+bodyClose, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-hottext identifier="h%x"><b/></qti-hottext>`, i)
		})
	}},
	{"template-decls", func(size int) []byte {
		return numberedTo(size, itemOpen, `<qti-item-body/></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-template-declaration identifier="T%x" cardinality="single" base-type="integer"/>`, i)
		})
	}},
	{"test-item-refs", func(size int) []byte {
		return numberedTo(size, `<qti-assessment-test xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="T" title="t"><qti-test-part identifier="P" navigation-mode="linear" submission-mode="individual"><qti-assessment-section identifier="S" title="s" visible="true">`,
			`</qti-assessment-section></qti-test-part></qti-assessment-test>`, func(b *bytes.Buffer, i int) {
				fmt.Fprintf(b, `<qti-assessment-item-ref identifier="I%x" href="i"/>`, i)
			})
	}},
	{"test-sections-nested", func(size int) []byte {
		return numberedTo(size, `<qti-assessment-test xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="T" title="t"><qti-test-part identifier="P" navigation-mode="linear" submission-mode="individual">`,
			`</qti-test-part></qti-assessment-test>`, func(b *bytes.Buffer, i int) {
				fmt.Fprintf(b, `<qti-assessment-section identifier="S%x" title="" visible="true"/>`, i)
			})
	}},
	{"utf16-empty-p", func(size int) []byte {
		return utf16Document(repeatTo(size/2, bodyOpen, `<p/>`, bodyClose))
	}},
	{"utf16-attrs", func(size int) []byte {
		return utf16Document(repeatTo(size/2, bodyOpen, `<p id="" class=""/>`, bodyClose))
	}},
	{"bom-empty-p", func(size int) []byte {
		return append([]byte("\xEF\xBB\xBF"), repeatTo(size, bodyOpen, `<p/>`, bodyClose)...)
	}},
}

// What a validation reserves covers what it takes, also for documents made
// to be counted wrong: namespace declarations in every form, CDATA sections,
// comments and processing instructions between text, character references,
// quotes and ">" in attribute values, other encodings, and the interactions
// and declarations whose identifiers the rules index. A review of the
// estimate found these shapes; two of them, choices of nothing but an
// identifier, took 5% more than was reserved before the estimate counted
// the attributes that are indexed.
//
// QTI_MEASURE_MEMORY=<bytes> sets the size of the documents, 1 MiB
// otherwise, and QTI_MEASURE_SHAPE picks the shapes whose name contains it.
func TestMemoryEstimateAdversarial(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("measures the heap: not with the race detector, which changes it, nor in short mode")
	}
	size := 1 << 20
	if env := os.Getenv("QTI_MEASURE_MEMORY"); env != "" {
		if _, err := fmt.Sscan(env, &size); err != nil || size <= 0 {
			t.Fatalf("QTI_MEASURE_MEMORY=%q: want a size in bytes", env)
		}
	}
	v, err := NewValidator(Options{Limits: qti.Limits{MaxDocumentSize: int64(size) * 2}})
	if err != nil {
		t.Fatal(err)
	}
	const mib = 1 << 20
	inMiB := func(n int64) float64 { return float64(n) / mib }
	only := os.Getenv("QTI_MEASURE_SHAPE")
	for _, shape := range adversarialShapes {
		if only != "" && !strings.Contains(shape.name, only) {
			continue
		}
		doc := shape.doc(size)
		sz := xpath.Measure(doc)
		p, res, elapsed := measure(v, doc)
		read, rules := p.peak[0].Load(), p.peak[1].Load()
		first := ""
		if len(res.Errors) > 0 {
			first = fmt.Sprintf("%s %.60s", res.Errors[0].Code, res.Errors[0].Message)
		}
		t.Logf("%-25s %5.2f MiB, %7d nodes (%d elements, %d attributes, %d texts, %d declarations) | read %5.1f of %5.1f | rules %5.1f of %5.1f | %4.1fs %v/%d %s",
			shape.name, inMiB(int64(len(doc))), sz.Nodes(), sz.Elements, sz.Attributes, sz.Texts, sz.Namespaces,
			inMiB(read), inMiB(p.reserved[0]), inMiB(rules), inMiB(p.reserved[1]), elapsed.Seconds(), res.Outcome, len(res.Errors), first)
		if read > p.reserved[0] {
			t.Errorf("%s: reading and checking the schema took %.1f MiB, more than the %.1f MiB reserved", shape.name, inMiB(read), inMiB(p.reserved[0]))
		}
		if res.Ran.Rules && rules > p.reserved[1] {
			t.Errorf("%s: the rules took %.1f MiB, more than the %.1f MiB reserved", shape.name, inMiB(rules), inMiB(p.reserved[1]))
		}
	}
}
