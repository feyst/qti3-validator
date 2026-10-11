package bootstrap

import (
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// cdataItem is a valid item whose one choice has its text in n CDATA
// sections.
func cdataItem(n int) string {
	return `<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false">` +
		`<qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/>` +
		`<qti-item-body><qti-choice-interaction response-identifier="RESPONSE" max-choices="1"><qti-simple-choice identifier="a">` +
		strings.Repeat("<![CDATA[aaaaaaaaaaaa]]>", n) +
		`</qti-simple-choice></qti-choice-interaction></qti-item-body></qti-assessment-item>`
}

// A text in many CDATA sections validates in time in proportion to its
// size: four times the sections take about four times as long. Each
// section used to be appended to the text so far, which took six seconds
// for 80,000 sections (2 MiB), and no limit stopped it.
func TestManyCDATASectionsValidateQuickly(t *testing.T) {
	v, err := NewValidator(Options{})
	if err != nil {
		t.Fatal(err)
	}
	n := 20_000
	if raceEnabled {
		n = 4000 // the race detector makes it slow, not quadratic
	}
	best := func(doc string) time.Duration {
		least := time.Duration(1<<63 - 1)
		for range 3 {
			start := time.Now()
			res := v.ValidateDocument(t.Context(), app.ValidateDocument{Document: strings.NewReader(doc), Size: int64(len(doc))})
			if res.Outcome != qti.OutcomeValid || len(res.Errors) != 0 {
				t.Fatalf("got %v, %+v", res.Outcome, res.Errors)
			}
			least = min(least, time.Since(start))
		}
		return least
	}
	a, b := best(cdataItem(n)), best(cdataItem(4*n))
	t.Logf("%d sections in %v, %d in %v", n, a, 4*n, b)
	if b > 9*a+50*time.Millisecond {
		t.Errorf("%d sections took %v and %d took %v: not linear", n, a, 4*n, b)
	}
}
