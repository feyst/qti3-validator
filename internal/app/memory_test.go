package app_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// fakeMemory is an app.Memory with a limit, that records what was reserved
// and fails with err once failAfter reservations that grow were granted.
type fakeMemory struct {
	limit     int64
	held      int64
	calls     []int64
	grows     int
	failAfter int // 0: never
	err       error
}

func (m *fakeMemory) Limit() int64 { return m.limit }

func (m *fakeMemory) Reserve(_ context.Context, n int64) error {
	m.calls = append(m.calls, n)
	if n > m.held {
		if n > m.limit {
			return app.ErrMemoryExceeded
		}
		if m.failAfter > 0 && m.grows >= m.failAfter {
			return m.err
		}
		m.grows++
	}
	m.held = n
	return nil
}

// denseItem is a valid item of n empty paragraphs: few bytes, many nodes.
func denseItem(n int) string {
	return `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false"><qti-item-body>` +
		strings.Repeat(`<p/>`, n) + `</qti-item-body></qti-assessment-item>`
}

func validateWith(t *testing.T, m app.Memory, doc string) qti.DocumentResult {
	t.Helper()
	ctx := app.WithMemory(context.Background(), m)
	return testValidator(t).ValidateDocument(ctx, app.ValidateDocument{Document: strings.NewReader(doc), Size: int64(len(doc))})
}

// A validation reserves little to read the document and check its schema,
// then what the document's nodes take for the rules, in that order.
func TestMemoryReservedInTwoSteps(t *testing.T) {
	m := &fakeMemory{limit: 1 << 40}
	doc := denseItem(20000) // 80 KiB, 20,000 nodes
	if res := validateWith(t, m, doc); !res.Valid || !res.Ran.Rules {
		t.Fatalf("got %+v", res)
	}
	if len(m.calls) != 3 {
		t.Fatalf("reservations %v, want one to read, one for the document as read and one for the rules", m.calls)
	}
	read, held, rules := m.calls[0], m.calls[1], m.calls[2]
	size := int64(len(doc))
	if read < 2*size || read > 2<<20 {
		t.Errorf("reserved %d bytes to read %d bytes", read, size)
	}
	if held >= read || held < size {
		t.Errorf("holds %d bytes for a document of %d once the schema check is over, after %d", held, size, read)
	}
	// 20,000 nodes of some 170 bytes and their indexes: some 5 MiB, 60 times
	// the size of the document.
	if rules < 20000*160 || rules > 8<<20 {
		t.Errorf("reserved %d bytes for the rules of 20,000 nodes", rules)
	}
	// The same bytes of prose are a few nodes, and take a fraction.
	prose := &fakeMemory{limit: 1 << 40}
	text := strings.Replace(denseItem(1), `<p/>`, `<p>`+strings.Repeat("word ", 16000)+`</p>`, 1)
	if res := validateWith(t, prose, text); !res.Valid {
		t.Fatalf("got %+v", res)
	}
	if got := prose.calls[len(prose.calls)-1]; got > rules/2 {
		t.Errorf("reserved %d bytes for %d bytes of prose, and %d for as many bytes of empty elements", got, len(text), rules)
	}
}

// A document whose nodes take more than a validation may reserve is refused
// as too large, with the numbers; its rules do not run.
func TestMemoryExceededByNodes(t *testing.T) {
	m := &fakeMemory{limit: 8 << 20}
	res := validateWith(t, m, denseItem(200000)) // 800 KiB, some 50 MiB of nodes
	// Refused at once: the schema check of so many nodes would take seconds.
	if res.Outcome != qti.OutcomeTooLarge || res.Ran.Rules || res.Ran.Schema || res.Schema == "" || res.Version == "" {
		t.Fatalf("got %+v", res)
	}
	last := res.Errors[len(res.Errors)-1]
	for _, want := range []string{"200 thousand nodes", "MiB of memory to check", "allows 8.0 MiB", "MAX_VALIDATION_MEMORY"} {
		if last.Code != qti.CodeTooLarge || !strings.Contains(last.Message, want) {
			t.Errorf("finding %s %q, want it to say %q", last.Code, last.Message, want)
		}
	}
	// The same memory takes a document of the same size with few nodes.
	text := strings.Replace(denseItem(1), `<p/>`, `<p>`+strings.Repeat("word ", 100000)+`</p>`, 1)
	if res := validateWith(t, &fakeMemory{limit: 8 << 20}, text); !res.Valid {
		t.Fatalf("500 KiB of prose in 8 MiB: %+v", res)
	}
}

// A document too large to read in the memory allowed is refused before it
// is read.
func TestMemoryExceededBySize(t *testing.T) {
	m := &fakeMemory{limit: 1 << 20}
	res := validateWith(t, m, denseItem(100000))
	if res.Outcome != qti.OutcomeTooLarge || res.Ran.Schema || len(res.Errors) != 1 ||
		!strings.Contains(res.Errors[0].Message, "to read and to check against its schema") {
		t.Fatalf("got %+v", res)
	}
}

// A validation that finds no memory in time stops like one that runs out of
// time, with what it found so far.
func TestMemoryBusyStopsValidation(t *testing.T) {
	m := &fakeMemory{limit: 1 << 40, failAfter: 1, err: app.ErrMemoryBusy}
	res := validateWith(t, m, denseItem(10))
	if res.Outcome != qti.OutcomeTooComplex || res.Ran.Rules || res.Errors[0].Code != qti.CodeTooComplex ||
		!strings.Contains(res.Errors[0].Message, "no memory free") {
		t.Fatalf("got %+v", res)
	}
}

// In a package every entry reserves for itself and gives it back, and an
// entry that cannot fit is an error of that file: the others are validated.
func TestMemoryPerPackageEntry(t *testing.T) {
	entries := packageEntries(t)
	entries = append(entries, entry{"dense.xml", denseItem(200000)})
	data := makeZip(t, entries...)
	m := &fakeMemory{limit: 6 << 20}
	ctx := app.WithMemory(context.Background(), m)
	res := testValidator(t).ValidatePackage(ctx, app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
	if res.Outcome != qti.OutcomeInvalid {
		t.Fatalf("outcome %v, errors %v", res.Outcome, res.Errors)
	}
	for _, f := range res.Files {
		if f.File != "dense.xml" {
			if !f.Valid {
				t.Errorf("%s: %v", f.File, f.Errors)
			}
			continue
		}
		if f.Valid || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeTooLarge || f.Errors[0].File != "dense.xml" ||
			!strings.Contains(f.Errors[0].Message, "200 thousand nodes") {
			t.Errorf("dense.xml: %+v", f)
		}
	}
	if m.held != 0 {
		t.Errorf("%d bytes still reserved after the package", m.held)
	}
	released := 0
	for _, n := range m.calls {
		if n == 0 {
			released++
		}
	}
	xml := 0
	for _, e := range entries {
		if strings.HasSuffix(e.name, ".xml") {
			xml++
		}
	}
	if released < xml {
		t.Errorf("%d releases for %d XML entries: %v", released, xml, m.calls)
	}
}
