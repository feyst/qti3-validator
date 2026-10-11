package app_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// fakeMemory is an app.Memory with a limit, that records what was reserved.
type fakeMemory struct {
	limit int64
	held  int64
	calls []int64 // what was reserved, in order; 0 is a release
	// noRoom makes TryReserve find no room for more, so the validation has
	// to wait, with Reserve; waitErr is what waiting then ends in.
	noRoom  bool
	waitErr error
	waits   []int64 // what was held when a wait began
}

func (m *fakeMemory) Limit() int64 { return m.limit }

func (m *fakeMemory) TryReserve(n int64) bool {
	if n > m.held && (m.noRoom || n > m.limit) {
		return false
	}
	m.held = n
	m.calls = append(m.calls, n)
	return true
}

func (m *fakeMemory) Reserve(_ context.Context, n int64) error {
	if n > m.limit {
		m.held = 0
		return app.ErrMemoryExceeded
	}
	if len(m.calls) > 0 && m.calls[len(m.calls)-1] != 0 {
		// Not the first reservation of a document: it waits, with nothing.
		m.waits = append(m.waits, m.held)
		m.held = 0
		if m.waitErr != nil {
			return m.waitErr
		}
	}
	m.held = n
	m.calls = append(m.calls, n)
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
	if len(m.calls) != 2 || len(m.waits) != 0 {
		t.Fatalf("reservations %v, waits %v: want one to read and one for the rules", m.calls, m.waits)
	}
	read, rules := m.calls[0], m.calls[1]
	size := int64(len(doc))
	if read < 2*size || read > 2<<20 {
		t.Errorf("reserved %d bytes to read %d bytes", read, size)
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

// onceReader hides that a reader can be read again.
type onceReader struct{ io.Reader }

// A validation that finds no room for the rules waits for it with nothing:
// it gives up what it reserved, lets go of the document, and reads it again
// once it has its room. One that cannot read its document again stops.
func TestMemoryWaitsWithoutTheDocument(t *testing.T) {
	doc := denseItem(1000) + "\n"
	want := validateString(t, doc, "")
	m := &fakeMemory{limit: 1 << 40, noRoom: true}
	res := validateWith(t, m, doc)
	if !res.Valid || !res.Ran.Rules || len(res.Errors) != len(want.Errors) {
		t.Fatalf("got %+v", res)
	}
	if len(m.waits) != 1 || len(m.calls) != 2 {
		t.Fatalf("reservations %v, waits %v: want one wait, for the rules", m.calls, m.waits)
	}

	m = &fakeMemory{limit: 1 << 40, noRoom: true}
	ctx := app.WithMemory(context.Background(), m)
	res = testValidator(t).ValidateDocument(ctx, app.ValidateDocument{Document: onceReader{strings.NewReader(doc)}, Size: int64(len(doc))})
	if res.Outcome != qti.OutcomeTooComplex || res.Ran.Rules || len(m.waits) != 0 {
		t.Fatalf("a document that cannot be read again: %+v, waits %v", res, m.waits)
	}

	// In a package the entry is read again.
	entries := packageEntries(t)
	data := makeZip(t, entries...)
	m = &fakeMemory{limit: 1 << 40, noRoom: true}
	ctx = app.WithMemory(context.Background(), m)
	pkg := testValidator(t).ValidatePackage(ctx, app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
	if !pkg.Valid || len(m.waits) == 0 || m.held != 0 {
		t.Fatalf("package: valid %v, errors %v, files %+v, waits %v, held %d", pkg.Valid, pkg.Errors, pkg.Files, m.waits, m.held)
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
	m := &fakeMemory{limit: 1 << 40, noRoom: true, waitErr: app.ErrMemoryBusy}
	res := validateWith(t, m, denseItem(10))
	last := res.Errors[len(res.Errors)-1]
	if res.Outcome != qti.OutcomeTooComplex || res.Ran.Rules || last.Code != qti.CodeTooComplex ||
		!strings.Contains(last.Message, "no memory free") || m.held != 0 {
		t.Fatalf("got %+v, held %d", res, m.held)
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
