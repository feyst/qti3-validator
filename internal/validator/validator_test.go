package validator

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

var (
	sharedOnce      sync.Once
	sharedValidator *Validator
	sharedErr       error
)

// testValidator compiles the embedded schemas once for the whole package.
func testValidator(t testing.TB) *Validator {
	t.Helper()
	sharedOnce.Do(func() { sharedValidator, sharedErr = New(Options{}) })
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return sharedValidator
}

func validateFile(t *testing.T, v *Validator, name string) ValidationResult {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return v.Validate(context.Background(), f, ValidateOptions{})
}

func TestValidAssessmentItem(t *testing.T) {
	res := validateFile(t, testValidator(t), "valid/assessment-item.xml")
	if !res.Valid || res.Outcome != OutcomeValid || res.Schema != "qti-assessment-item" || len(res.Errors) != 0 {
		t.Fatalf("got %+v", res)
	}
}

func TestInvalidDocuments(t *testing.T) {
	tests := []struct {
		file    string
		outcome Outcome
		code    string
		schema  string
		message string
	}{
		{"invalid/malformed.xml", OutcomeMalformed, CodeInvalidXML, "qti-assessment-item", ""},
		{"invalid/missing-required-element.xml", OutcomeInvalid, CodeValidation, "qti-assessment-item", ""},
		{"invalid/invalid-attribute.xml", OutcomeInvalid, CodeValidation, "qti-assessment-item", "response-identifier"},
		{"invalid/invalid-value.xml", OutcomeInvalid, CodeValidation, "qti-assessment-item", "shuffle"},
		{"invalid/invalid-cardinality.xml", OutcomeInvalid, CodeValidation, "qti-assessment-item", ""},
		{"invalid/wrong-namespace.xml", OutcomeInvalid, CodeUnsupportedDocument, "", "imsqti_v2p2"},
		{"invalid/unknown-root.xml", OutcomeInvalid, CodeUnsupportedDocument, "", "qti-item-body"},
	}
	v := testValidator(t)
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			res := validateFile(t, v, tt.file)
			if res.Valid || res.Outcome != tt.outcome || res.Schema != tt.schema {
				t.Fatalf("got valid=%v outcome=%v schema=%q, want outcome %v schema %q: %+v",
					res.Valid, res.Outcome, res.Schema, tt.outcome, tt.schema, res.Errors)
			}
			if len(res.Errors) == 0 || res.Errors[0].Code != tt.code {
				t.Fatalf("want first error code %q, got %+v", tt.code, res.Errors)
			}
			if !strings.Contains(res.Errors[0].Message, tt.message) {
				t.Fatalf("message %q does not mention %q", res.Errors[0].Message, tt.message)
			}
			if tt.code == CodeValidation && (res.Errors[0].Line == 0 || res.Errors[0].Column == 0) {
				t.Fatalf("validation error without position: %+v", res.Errors[0])
			}
		})
	}
}

func TestMultipleValidationErrors(t *testing.T) {
	res := validateFile(t, testValidator(t), "invalid/multiple-errors.xml")
	if res.Outcome != OutcomeInvalid || len(res.Errors) < 3 {
		t.Fatalf("want at least 3 errors, got %+v", res)
	}
}

func TestMaxErrors(t *testing.T) {
	v := *testValidator(t)
	v.limits.MaxErrors = 1
	res := validateFile(t, &v, "invalid/multiple-errors.xml")
	if res.Outcome != OutcomeInvalid || len(res.Errors) != 1 {
		t.Fatalf("want exactly 1 error, got %+v", res.Errors)
	}
}

func TestDocumentTooLarge(t *testing.T) {
	v := *testValidator(t)
	v.limits.MaxDocumentSize = 200
	res := validateFile(t, &v, "valid/assessment-item.xml")
	if res.Outcome != OutcomeTooLarge || res.Errors[0].Code != CodeTooLarge {
		t.Fatalf("got %+v", res)
	}
}

func TestDepthLimit(t *testing.T) {
	v := *testValidator(t)
	v.limits.MaxDepth = 3
	res := validateFile(t, &v, "valid/assessment-item.xml")
	if res.Valid || res.Errors[0].Code != CodeLimitExceeded {
		t.Fatalf("got %+v", res)
	}
}

func TestRejectsDTDAndExternalEntities(t *testing.T) {
	docs := map[string]string{
		"external entity": `<?xml version="1.0"?>
<!DOCTYPE qti-assessment-item [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="x" title="&xxe;" time-dependent="false"/>`,
		"entity expansion": `<!DOCTYPE a [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0">&b;</qti-assessment-item>`,
	}
	v := testValidator(t)
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			res := v.Validate(context.Background(), strings.NewReader(doc), ValidateOptions{})
			if res.Valid || res.Errors[0].Code != CodeUnsupportedXML {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

func TestRejectsNonUTF8(t *testing.T) {
	doc := `<?xml version="1.0" encoding="ISO-8859-1"?><qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>`
	res := testValidator(t).Validate(context.Background(), strings.NewReader(doc), ValidateOptions{})
	if res.Valid || res.Errors[0].Code != CodeUnsupportedEncoding {
		t.Fatalf("got %+v", res)
	}
}

// The XSD library carries the text of a parse error in its cause.
func TestMalformedMessageIsNotEmpty(t *testing.T) {
	doc := readTestdata(t, "valid/assessment-item.xml")
	res := validateString(t, doc[:len(doc)-30], ValidateOptions{})
	if res.Outcome != OutcomeMalformed || res.Errors[0].Message == "" || res.Errors[0].Line == 0 {
		t.Fatalf("got %+v", res.Errors)
	}
}

func TestMalformedWithUnknownRootIsMalformed(t *testing.T) {
	res := testValidator(t).Validate(context.Background(), strings.NewReader(`<a><b></a>`), ValidateOptions{})
	if res.Outcome != OutcomeMalformed || res.Errors[0].Code != CodeInvalidXML {
		t.Fatalf("got %+v", res)
	}
}

func TestEmptyBody(t *testing.T) {
	res := testValidator(t).Validate(context.Background(), strings.NewReader(""), ValidateOptions{})
	if res.Outcome != OutcomeMalformed {
		t.Fatalf("got %+v", res)
	}
}

func TestConcurrentValidation(t *testing.T) {
	v := testValidator(t)
	valid, err := os.ReadFile("../../testdata/valid/assessment-item.xml")
	if err != nil {
		t.Fatal(err)
	}
	invalid, err := os.ReadFile("../../testdata/invalid/invalid-value.xml")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			doc, want := valid, true
			if i%2 == 1 {
				doc, want = invalid, false
			}
			if res := v.Validate(context.Background(), strings.NewReader(string(doc)), ValidateOptions{}); res.Valid != want {
				t.Errorf("goroutine %d: got %+v", i, res)
			}
		}()
	}
	wg.Wait()
}

func TestMissingSchemaDependencyFailsAtStartup(t *testing.T) {
	const missing = "purl.imsglobal.org/spec/mathml/v3p0/schema/xsd/mathml3-common.xsd.gz"
	fsys := fstest.MapFS{}
	err := fs.WalkDir(SchemaFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || p == missing {
			return err
		}
		data, err := fs.ReadFile(SchemaFS(), p)
		fsys[p] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(Options{FS: fsys})
	if err == nil || !strings.Contains(err.Error(), "mathml3-common.xsd") {
		t.Fatalf("want an error naming the missing schema, got %v", err)
	}
}

// Every schema the compiler loads must be pinned in the lock file, and the
// override must target a pinned URL.
func TestLoadedSchemasArePinned(t *testing.T) {
	pinned := map[string]bool{}
	f, err := os.Open("schemas/schemas.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			pinned[fields[1]] = true
		}
	}
	var loaded []string
	if _, err := New(Options{OnSchemaLoaded: func(u string) { loaded = append(loaded, u) }}); err != nil {
		t.Fatal(err)
	}
	if len(loaded) == 0 {
		t.Fatal("no schemas loaded")
	}
	for _, u := range loaded {
		if !pinned[u] {
			t.Errorf("loaded schema %s is not in schemas.lock", u)
		}
	}
	for u := range overrides {
		if !pinned[u] {
			t.Errorf("override %s is not in schemas.lock", u)
		}
	}
}

func TestEveryDocumentTypeIsDeclared(t *testing.T) {
	v := testValidator(t)
	for _, dt := range DocumentTypes {
		doc := `<` + dt.Root + ` xmlns="` + dt.Namespace + `"/>`
		res := v.Validate(context.Background(), strings.NewReader(doc), ValidateOptions{})
		for _, e := range res.Errors {
			if e.Code == CodeUnsupportedDocument || e.Code == CodeUnsupportedXML ||
				strings.Contains(e.Message, "no declaration") || strings.Contains(e.Message, "not declared") {
				t.Errorf("%s: root is not declared in the schemas: %+v", dt.Root, e)
			}
		}
	}
}
