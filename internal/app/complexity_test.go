package app_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"qti3-validator/internal/app"
	"qti3-validator/internal/bootstrap"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/testutil"
)

var (
	slowOnce      sync.Once
	slowValidator *app.Validator
	slowErr       error
)

// testSlowValidator is the built-in validator plus testutil.SlowRules.
func testSlowValidator(t testing.TB) *app.Validator {
	t.Helper()
	slowOnce.Do(func() {
		dir := writeDir(t, map[string]string{"slow.sch": testutil.SlowRules})
		slowValidator, slowErr = bootstrap.NewValidator(bootstrap.Options{ValidatorsDir: dir})
	})
	if slowErr != nil {
		t.Fatal(slowErr)
	}
	return slowValidator
}

// A 1EdTech rule with absolute paths in its predicates took 44 seconds on
// an item with 400 declarations and interactions; each path is now
// evaluated once per document.
func TestAbsolutePathsInPredicatesAreFast(t *testing.T) {
	v := testValidator(t)
	start := time.Now()
	res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(testutil.ItemWithDeclarations(400))})
	if took := time.Since(start); took > time.Second {
		t.Errorf("took %v", took)
	}
	fired := false
	for _, e := range res.Errors {
		fired = fired || strings.HasPrefix(e.Rule, "RULE_SET_SPECIFIC_CLASS_RESPONSEDEC")
	}
	if res.Outcome != qti.OutcomeInvalid || !fired {
		t.Fatalf("got %+v", res)
	}
}

// A validation that runs out of time stops promptly, with the findings so
// far: the error the additional checks found before the slow rule ran.
// Without the deadline the slow rule runs until its budget of some 100
// million steps runs out, seconds of work and more under -race; the message
// shows the deadline stopped it, and the margin allows for a loaded runner.
func TestDeadlineStopsValidation(t *testing.T) {
	v := testSlowValidator(t)
	const timeout = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	start := time.Now()
	res := v.ValidateDocument(ctx, app.ValidateDocument{Document: strings.NewReader(testutil.ItemWithChoices(300))})
	if late := time.Since(start) - timeout; late > time.Second {
		t.Errorf("stopped %v after the deadline", late)
	}
	last := res.Errors[len(res.Errors)-1]
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Errors) < 2 || last.Code != qti.CodeTooComplex ||
		!strings.Contains(last.Message, "REQUEST_TIMEOUT") || res.Errors[0].Code != qti.CodeSchematron {
		t.Fatalf("got %+v", res)
	}
}

// In a package, the document that ran out of time reports it, and the
// package says which files were not validated.
func TestDeadlineStopsPackage(t *testing.T) {
	v := testSlowValidator(t)
	data := makeZip(t,
		entry{"imsmanifest.xml", readTestdata(t, "package/imsmanifest.xml")},
		entry{"slow.xml", testutil.ItemWithChoices(300)},
		entry{"items/item-1.xml", readTestdata(t, "package/item-1.xml")},
	)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	res := v.ValidatePackage(ctx, app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
	if res.Valid || res.Outcome != qti.OutcomeTooComplex || len(res.Files) != 2 || len(res.Errors) != 1 ||
		!strings.Contains(res.Errors[0].Message, "items/item-1.xml and the files after it were not validated") {
		t.Fatalf("got %+v", res)
	}
	f := fileResult(t, res, "slow.xml")
	if last := f.Errors[len(f.Errors)-1]; f.Valid || last.Code != qti.CodeTooComplex || last.File != "slow.xml" {
		t.Fatalf("slow.xml: %+v", f)
	}
}

// Plain content up to MAX_REQUEST_SIZE validates: the budget of the rules
// grows with the document. It takes seconds, nearly a minute under -race,
// so it is skipped then and with -short; TestBudgetGrowsWithDocument in
// internal/adapter/rules checks the same at a hundredth of the size.
func TestLinearDocumentAtMaxSize(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("slow; runs without -short and -race")
	}
	doc := testutil.LinearItem(int(qti.DefaultLimits.MaxDocumentSize))
	res := testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
	if !res.Valid || res.Outcome != qti.OutcomeValid {
		t.Fatalf("%d bytes: got %v with %+v", len(doc), res.Outcome, res.Errors)
	}
}
