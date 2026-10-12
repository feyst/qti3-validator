package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xpath"
)

const itemOpen = `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false">`

func repeatTo(size int, head, unit, tail string) []byte {
	n := max(1, (size-len(head)-len(tail))/len(unit))
	var b bytes.Buffer
	b.Grow(size)
	b.WriteString(head)
	for range n {
		b.WriteString(unit)
	}
	b.WriteString(tail)
	return b.Bytes()
}

func numberedTo(size int, head, tail string, unit func(b *bytes.Buffer, i int)) []byte {
	var b bytes.Buffer
	b.Grow(size)
	b.WriteString(head)
	for i := 0; b.Len() < size-len(tail)-200; i++ {
		unit(&b, i)
	}
	b.WriteString(tail)
	return b.Bytes()
}

var memoryShapes = []struct {
	name string
	doc  func(size int) []byte
}{
	{"correct-response", func(size int) []byte {
		n := size / 263
		var b bytes.Buffer
		b.WriteString(itemOpen)
		for i := range n {
			fmt.Fprintf(&b, `<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"/>`, i)
		}
		b.WriteString(`<qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/><qti-item-body/><qti-response-processing>`)
		for i := range n {
			fmt.Fprintf(&b, `<qti-response-condition><qti-response-if><qti-match><qti-variable identifier="R%d"/><qti-correct identifier="R%d"/></qti-match></qti-response-if></qti-response-condition>`, i, i)
		}
		b.WriteString(`</qti-response-processing></qti-assessment-item>`)
		return b.Bytes()
	}},
	{"empty-p", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<p/>`, `</qti-item-body></qti-assessment-item>`)
	}},
	{"empty-b-in-p", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p>`, `<b/>`, `</p></qti-item-body></qti-assessment-item>`)
	}},
	{"p-text", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<p>x</p>`, `</qti-item-body></qti-assessment-item>`)
	}},
	{"b-text-b", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p>`, `<b/>x`, `</p></qti-item-body></qti-assessment-item>`)
	}},
	{"attributes", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<p id="a" dir="ltr" class="c" data-a="1" data-b="2"/>`, `</qti-item-body></qti-assessment-item>`)
	}},
	{"short-attrs", func(size int) []byte {
		var b bytes.Buffer
		b.WriteString(itemOpen + `<qti-item-body><p`)
		for i := 0; b.Len() < size-100; i++ {
			fmt.Fprintf(&b, ` data-%x=""`, i)
		}
		b.WriteString(`/></qti-item-body></qti-assessment-item>`)
		return b.Bytes()
	}},
	{"outcome-declarations", func(size int) []byte {
		return numberedTo(size, itemOpen, `<qti-item-body/></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
			fmt.Fprintf(b, `<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`, i)
		})
	}},
	{"choices", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/><qti-item-body><qti-choice-interaction response-identifier="RESPONSE">`,
			`</qti-choice-interaction></qti-item-body></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
				fmt.Fprintf(b, `<qti-simple-choice identifier="c%d">x</qti-simple-choice>`, i)
			})
	}},
	{"deep-nesting", func(size int) []byte {
		unit := strings.Repeat(`<div>`, 250) + strings.Repeat(`</div>`, 250)
		return repeatTo(size, itemOpen+`<qti-item-body>`, unit, `</qti-item-body></qti-assessment-item>`)
	}},
	{"big-text", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p>`, `A`, `</p></qti-item-body></qti-assessment-item>`)
	}},
	{"entities", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p>`, `&lt;`, `</p></qti-item-body></qti-assessment-item>`)
	}},
	{"prose", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<p>The quick brown fox jumps over the lazy dog, and <em>then</em> rests a while.</p>`, `</qti-item-body></qti-assessment-item>`)
	}},
	{"mathml", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p>`, `<math xmlns="http://www.w3.org/1998/Math/MathML"><mi>x</mi><mo>=</mo><mn>1</mn></math>`, `</p></qti-item-body></qti-assessment-item>`)
	}},
	{"mi-run", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body><p><math xmlns="http://www.w3.org/1998/Math/MathML">`, `<mi/>`, `</math></p></qti-item-body></qti-assessment-item>`)
	}},
	{"invalid-100-errors", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<x/>`, `</qti-item-body></qti-assessment-item>`)
	}},
	// Choices with an identifier, which the rules index by it.
	{"hottext", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-response-declaration identifier="R" cardinality="multiple" base-type="identifier"/><qti-item-body><qti-hottext-interaction response-identifier="R"><p>`,
			`</p></qti-hottext-interaction></qti-item-body></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
				fmt.Fprintf(b, `<qti-hottext identifier="h%x"/>`, i)
			})
	}},
	{"gaps", func(size int) []byte {
		return numberedTo(size, itemOpen+`<qti-item-body><qti-gap-match-interaction response-identifier="R"><p>`,
			`</p></qti-gap-match-interaction></qti-item-body></qti-assessment-item>`, func(b *bytes.Buffer, i int) {
				fmt.Fprintf(b, `<qti-gap identifier="g%x"/>`, i)
			})
	}},
	{"comments", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<!---->`, `</qti-item-body></qti-assessment-item>`)
	}},
}

// phases samples the live heap while a validation runs, apart for the time
// before and after the rules start. A validation reserves for its whole
// check once the document is read, and sets that to what the rules take
// when the schema check is over (app.Memory): the second reservation it
// makes without waiting.
type phases struct {
	phase    atomic.Int32
	peak     [2]atomic.Int64
	reserved [2]int64 // the most reserved in each phase
	tried    int
}

func (p *phases) Limit() int64 { return 1 << 60 }

func (p *phases) TryReserve(n int64) bool {
	if n > 0 {
		p.tried++
		if p.tried == 2 {
			// What the schema check left behind is garbage by now: it is
			// not what the rules take.
			runtime.GC()
			p.phase.Store(1)
		}
	}
	_ = p.Reserve(context.Background(), n)
	return true
}

func (p *phases) Reserve(_ context.Context, n int64) error {
	i := p.phase.Load()
	p.reserved[i] = max(p.reserved[i], n)
	return nil
}

// measure validates doc and returns what it reserved and what it really
// held, above the heap before it, with the collector kept busy so that the
// heap is little more than what is live.
func measure(v *app.Validator, doc []byte) (p *phases, res qti.DocumentResult, elapsed time.Duration) {
	old := debug.SetGCPercent(5)
	defer debug.SetGCPercent(old)
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	read := func() int64 { metrics.Read(sample); return int64(sample[0].Value.Uint64()) }
	base := read()
	p = &phases{}
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			top := &p.peak[p.phase.Load()]
			if h := read() - base; h > top.Load() {
				top.Store(h)
			}
			time.Sleep(100 * time.Microsecond)
		}
	}()
	start := time.Now()
	res = v.ValidateDocument(app.WithMemory(context.Background(), p), app.ValidateDocument{Document: bytes.NewReader(doc), Size: int64(len(doc))})
	elapsed = time.Since(start)
	close(stop)
	<-done
	return p, res, elapsed
}

// What a validation reserves in memory (app.Memory) is held to what it
// really takes, for documents of every shape: never less, or the documents
// in progress would not fit in the memory the service was given, and for
// the rules, which take most, not more than 1.5 times as much, or
// documents that fit would be refused. The estimate follows the layout of
// xpath.Node and the indexes of the rules, so a change there that makes
// documents larger or smaller in memory fails here, and the constants
// (xpath.Size.DOMBytes, rules.Checker.EstimateMemory, app's memoryToRead)
// are then measured again.
//
// QTI_MEASURE_MEMORY=<bytes> sets the size of the documents, 1 MiB
// otherwise; with -v the test prints the table of docs/deployment.md.
func TestMemoryEstimate(t *testing.T) {
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
	t.Logf("%-22s %6s %8s | %8s %8s | %8s %8s %6s %7s | %7s %6s %s", "shape", "MiB", "nodes",
		"read", "reserved", "rules", "reserved", "ratio", "x size", "scan ms", "s", "outcome/errors")
	for _, shape := range memoryShapes {
		doc := shape.doc(size)
		start := time.Now()
		nodes := xpath.Measure(doc).Nodes()
		scan := time.Since(start)
		p, res, elapsed := measure(v, doc)
		read, rules := p.peak[0].Load(), p.peak[1].Load()
		ratio := 0.0
		if rules > 0 {
			ratio = float64(p.reserved[1]) / float64(rules)
		}
		t.Logf("%-22s %6.2f %8d | %8.1f %8.1f | %8.1f %8.1f %6.2f %7.1f | %7.2f %6.2f %v/%d", shape.name, inMiB(int64(len(doc))), nodes,
			inMiB(read), inMiB(p.reserved[0]), inMiB(rules), inMiB(p.reserved[1]), ratio, float64(rules)/float64(len(doc)),
			float64(scan.Microseconds())/1000, elapsed.Seconds(), res.Outcome, len(res.Errors))
		if read > p.reserved[0] {
			t.Errorf("%s: reading and checking the schema took %.1f MiB, more than the %.1f MiB reserved",
				shape.name, inMiB(read), inMiB(p.reserved[0]))
		}
		if !res.Ran.Rules {
			continue
		}
		if rules > p.reserved[1] {
			t.Errorf("%s: the rules took %.1f MiB, more than the %.1f MiB reserved", shape.name, inMiB(rules), inMiB(p.reserved[1]))
		}
		// A small tree is all base: the bound is for documents that matter.
		// It is 1.5 times, and a tenth for the noise of the measurement.
		if rules > 8*mib && p.reserved[1] > rules*16/10 {
			t.Errorf("%s: %.1f MiB reserved for the rules, more than 1.6 times the %.1f MiB they took",
				shape.name, inMiB(p.reserved[1]), inMiB(rules))
		}
		if scan > elapsed/10 {
			t.Errorf("%s: counting the nodes took %v of the %v of the validation", shape.name, scan, elapsed)
		}
	}
}

// TestMemoryLargestDocuments prints, for the table of docs/deployment.md,
// the largest document of each shape that fits a budget:
// QTI_MEASURE_BUDGETS=208,464,976 lists the budgets in MiB. What a document
// reserves grows in a straight line with its size, so two sizes tell.
func TestMemoryLargestDocuments(t *testing.T) {
	env := os.Getenv("QTI_MEASURE_BUDGETS")
	if env == "" {
		t.Skip("set QTI_MEASURE_BUDGETS to budgets in MiB, separated by commas")
	}
	v, err := NewValidator(Options{Limits: qti.Limits{MaxDocumentSize: 8 << 20}})
	if err != nil {
		t.Fatal(err)
	}
	const mib = 1 << 20
	reserved := func(doc []byte) float64 {
		p, _, _ := measure(v, doc)
		return float64(max(p.reserved[0], p.reserved[1]))
	}
	for _, shape := range memoryShapes {
		small, large := shape.doc(mib), shape.doc(2*mib)
		a, b := reserved(small), reserved(large)
		slope := (b - a) / float64(len(large)-len(small))
		base := a - slope*float64(len(small))
		var line strings.Builder
		fmt.Fprintf(&line, "%-22s %6.1f bytes a byte |", shape.name, slope)
		for field := range strings.SplitSeq(env, ",") {
			var budget float64
			if _, err := fmt.Sscan(field, &budget); err != nil {
				t.Fatalf("QTI_MEASURE_BUDGETS: %v", err)
			}
			fmt.Fprintf(&line, " %4.0f MiB: %6.1f MiB |", budget, (budget*mib-base)/slope/mib)
		}
		t.Log(line.String())
	}
}

// namespaceAxisRules is a Schematron file for a validators directory whose
// rule walks the namespace axis of every element.
const namespaceAxisRules = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <sch:pattern id="NS">
    <sch:rule context="*">
      <sch:assert test="count(namespace::*) &lt; 100">Too many namespaces in scope.</sch:assert>
    </sch:rule>
  </sch:pattern>
</sch:schema>`

// Rules with a step on the namespace axis build the namespace nodes of the
// elements they visit, one for every namespace in scope, which the document
// does not hold otherwise. The estimate counts them when the rules that are
// loaded use that axis, and only then.
func TestMemoryEstimateNamespaceAxis(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("measures the heap: not with the race detector, which changes it, nor in short mode")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "namespaces.sch"), []byte(namespaceAxisRules), 0o600); err != nil {
		t.Fatal(err)
	}
	mounted, err := NewValidator(Options{ValidatorsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	builtin, err := NewValidator(Options{})
	if err != nil {
		t.Fatal(err)
	}
	// Six bindings in scope of 130,000 elements.
	open := strings.Replace(itemOpen, `<qti-assessment-item `, `<qti-assessment-item xmlns:a="urn:a" xmlns:b="urn:b" xmlns:c="urn:c" xmlns:d="urn:d" xmlns:e="urn:e" `, 1)
	doc := repeatTo(1<<20, open+`<qti-item-body>`, `<p><b/></p>`, `</qti-item-body></qti-assessment-item>`)
	const mib = 1 << 20
	plain, res, _ := measure(builtin, doc)
	if !res.Valid {
		t.Fatalf("got %+v", res)
	}
	p, res, _ := measure(mounted, doc)
	if !res.Valid || !res.Ran.Rules {
		t.Fatalf("got %+v", res)
	}
	took, reserved := p.peak[1].Load(), p.reserved[1]
	t.Logf("rules with the namespace axis took %.1f MiB, %.1f MiB reserved (%.2f×); without it %.1f MiB taken, %.1f MiB reserved",
		float64(took)/mib, float64(reserved)/mib, float64(reserved)/float64(took), float64(plain.peak[1].Load())/mib, float64(plain.reserved[1])/mib)
	if took > reserved || reserved > took*3/2 {
		t.Errorf("the rules took %.1f MiB with %.1f MiB reserved", float64(took)/mib, float64(reserved)/mib)
	}
	if plain.reserved[1] >= reserved-50*mib {
		t.Errorf("%.1f MiB reserved without rules on the namespace axis, %.1f MiB with them: want the namespace nodes counted only with them",
			float64(plain.reserved[1])/mib, float64(reserved)/mib)
	}
}
