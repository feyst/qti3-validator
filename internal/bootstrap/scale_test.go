package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/app"
)

// An interaction with many choices, many declarations or a test with many
// references must validate in time linear in their number: the uniqueness
// checks used to compare every element with every other, which took
// minutes for 20,000 choices.
func TestManyIdentifiersValidateQuickly(t *testing.T) {
	v, err := NewValidator(Options{})
	if err != nil {
		t.Fatal(err)
	}
	const n = 20000
	item := `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false">`
	choices := func(dups ...int) string {
		var b strings.Builder
		b.WriteString(item + `<qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/>`)
		b.WriteString(`<qti-item-body><qti-choice-interaction response-identifier="RESPONSE">`)
		for i := range n {
			fmt.Fprintf(&b, `<qti-simple-choice identifier="c%d">x</qti-simple-choice>`, i)
		}
		for _, i := range dups {
			fmt.Fprintf(&b, `<qti-simple-choice identifier="c%d">x</qti-simple-choice>`, i)
		}
		b.WriteString(`</qti-choice-interaction></qti-item-body></qti-assessment-item>`)
		return b.String()
	}
	declarations := func() string {
		var b strings.Builder
		b.WriteString(item + `<qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/>`)
		for i := range n {
			fmt.Fprintf(&b, `<qti-outcome-declaration identifier="O%d" cardinality="single" base-type="float"/>`, i)
		}
		b.WriteString(`<qti-item-body><qti-choice-interaction response-identifier="RESPONSE"><qti-simple-choice identifier="a">A</qti-simple-choice></qti-choice-interaction></qti-item-body></qti-assessment-item>`)
		return b.String()
	}
	references := func() string {
		var b strings.Builder
		b.WriteString(`<qti-assessment-test xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="T" title="t">`)
		b.WriteString(`<qti-test-part identifier="P" navigation-mode="linear" submission-mode="individual"><qti-assessment-section identifier="S" title="s" visible="true">`)
		for i := range n {
			fmt.Fprintf(&b, `<qti-assessment-item-ref identifier="I%d" href="i%d.xml"/>`, i, i)
		}
		b.WriteString(`</qti-assessment-section></qti-test-part></qti-assessment-test>`)
		return b.String()
	}
	dupMessage := func(i int) string {
		return fmt.Sprintf("Choice identifier 'c%d' is used by more than one choice in the same interaction.", i)
	}
	for _, tc := range []struct {
		name string
		doc  string
		want []string // error messages, in order
	}{
		{"choices", choices(), nil},
		{"duplicate choices", choices(7, 12345), []string{dupMessage(7), dupMessage(12345), dupMessage(7), dupMessage(12345)}},
		{"declarations", declarations(), nil},
		{"item references", references(), nil},
	} {
		start := time.Now()
		res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(tc.doc)})
		took := time.Since(start)
		t.Logf("%s: %d bytes, %v", tc.name, len(tc.doc), took)
		var got []string
		for _, e := range res.Errors {
			got = append(got, e.Message)
		}
		if !slices.Equal(got, tc.want) || res.Valid != (tc.want == nil) {
			t.Errorf("%s: valid %v, errors %q; want %q", tc.name, res.Valid, got, tc.want)
		}
		// Generous for the race detector and slow machines; quadratic
		// checks took minutes.
		if took > 10*time.Second {
			t.Errorf("%s: took %v", tc.name, took)
		}
	}
}
