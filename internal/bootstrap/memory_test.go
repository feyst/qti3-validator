package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/adapter/xsdschema"
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
	{"comments", func(size int) []byte {
		return repeatTo(size, itemOpen+`<qti-item-body>`, `<!---->`, `</qti-item-body></qti-assessment-item>`)
	}},
}

// peakHeap runs f and returns the most live heap (objects) seen while it
// ran, above the heap before it, sampled with the collector kept busy.
func peakHeap(f func()) (peak int64, elapsed time.Duration) {
	old := debug.SetGCPercent(5)
	defer debug.SetGCPercent(old)
	runtime.GC()
	sample := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
	read := func() int64 { metrics.Read(sample); return int64(sample[0].Value.Uint64()) }
	base := read()
	var top atomic.Int64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if v := read(); v > top.Load() {
				top.Store(v)
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	start := time.Now()
	f()
	elapsed = time.Since(start)
	close(stop)
	<-done
	return top.Load() - base, elapsed
}

// TestMeasureMemory prints what validating a document of each shape takes
// in memory. It is a measurement, not a test: QTI_MEASURE_MEMORY=<bytes>
// selects the document size and runs it.
func TestMeasureMemory(t *testing.T) {
	var size int
	if _, err := fmt.Sscan(os.Getenv("QTI_MEASURE_MEMORY"), &size); err != nil || size <= 0 {
		t.Skip("set QTI_MEASURE_MEMORY to a document size in bytes")
	}
	only := os.Getenv("QTI_MEASURE_SHAPE")
	v, err := NewValidator(Options{Limits: qti.Limits{MaxDocumentSize: int64(size)*2 + 1024}})
	if err != nil {
		t.Fatal(err)
	}
	schema, err := xsdschema.CompileVersion(xsdschema.Resolver{Store: schemastore.Embedded()}, qti.LatestVersion())
	if err != nil {
		t.Fatal(err)
	}
	const mib = 1 << 20
	t.Logf("%-22s %7s %8s %9s | %8s %6s | %8s %6s | %8s %6s | %6s %s", "shape", "MiB", "nodes", "B/node",
		"xsd MiB", "x", "dom MiB", "x", "all MiB", "x", "s", "outcome")
	for _, shape := range memoryShapes {
		if only != "" && only != shape.name {
			continue
		}
		doc := shape.doc(size)
		xsdPeak, _ := peakHeap(func() { schema.CheckSchema(doc, v.Limits()) })
		tree, _ := xpath.Parse(bytes.NewReader(doc))
		nodes := 0
		if tree != nil {
			nodes = tree.Nodes
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(tree)
		tree = nil
		runtime.GC()
		runtime.ReadMemStats(&before)
		dom := int64(after.HeapAlloc) - int64(before.HeapAlloc)
		var res qti.DocumentResult
		all, elapsed := peakHeap(func() {
			res = v.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(doc)})
		})
		f := func(n int64) float64 { return float64(n) / float64(len(doc)) }
		perNode := 0.0
		if nodes > 0 {
			perNode = float64(dom) / float64(nodes)
		}
		t.Logf("%-22s %7.2f %8d %9.0f | %8.1f %6.1f | %8.1f %6.1f | %8.1f %6.1f | %6.2f %v/%d", shape.name, float64(len(doc))/mib, nodes, perNode,
			float64(xsdPeak)/mib, f(xsdPeak), float64(dom)/mib, f(dom), float64(all)/mib, f(all), elapsed.Seconds(), res.Outcome, len(res.Errors))
		if len(res.Errors) > 0 {
			t.Logf("    first error: %s", res.Errors[0].Message)
		}
	}
}
