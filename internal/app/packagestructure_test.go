package app_test

import (
	"context"
	"strings"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
)

// withResources returns the test package with resources added to its
// manifest and the given files added or replaced.
func withResources(t *testing.T, resources string, files ...entry) []entry {
	t.Helper()
	entries := packageEntries(t)
	entries[0].data = strings.Replace(entries[0].data, "  </resources>", resources+"\n  </resources>", 1)
	for _, f := range files {
		replaced := false
		for i := range entries {
			if entries[i].name == f.name {
				entries[i].data, replaced = f.data, true
			}
		}
		if !replaced {
			entries = append(entries, f)
		}
	}
	return entries
}

// includeFragment makes item-1 include the fragment at href.
func includeFragment(t *testing.T, href string) entry {
	t.Helper()
	item := strings.Replace(readTestdata(t, "package/item-1.xml"), "<qti-item-body>",
		`<qti-item-body><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="`+href+`"/>`, 1)
	return entry{"items/item-1.xml", item}
}

const fragmentResource = `<resource identifier="frag" type="imsqti_fragment_xmlv3p0" href="items/frag.xml"><file href="items/frag.xml"/></resource>`

// A fragment an item includes is QTI content, not a QTI document: it is
// valid, and validated against the schema when its root is a global
// element of it.
func TestPackageFragmentResource(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		valid          bool
		code           qti.Code
	}{
		{"div", `<div xmlns="` + qti.NamespaceASI + `"><p>Shared text</p></div>`, true, ""},
		{"div with an interaction", `<div xmlns="` + qti.NamespaceASI + `"><qti-hottext-interaction response-identifier="RESPONSE" max-choices="1">` +
			`<p>The <qti-hottext identifier="A">quick</qti-hottext> fox.</p></qti-hottext-interaction></div>`, true, ""},
		// Not a global element of the schema: validated inside a
		// qti-response-processing (TestFragmentRoot).
		{"response condition", `<qti-response-condition xmlns="` + qti.NamespaceASI + `"><qti-response-if><qti-null/>` +
			`<qti-set-outcome-value identifier="SCORE"><qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value>` +
			`</qti-response-if></qti-response-condition>`, true, ""},
		{"invalid content", `<div xmlns="` + qti.NamespaceASI + `"><bogus/></div>`, false, qti.CodeValidation},
		{"other namespace", `<div xmlns="http://www.w3.org/1999/xhtml"><p>x</p></div>`, false, qti.CodeUnsupportedDocument},
		{"not well-formed", `<div xmlns="` + qti.NamespaceASI + `"><p></div>`, false, qti.CodeInvalidXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := validatePackageWith(t, makeZip(t, withResources(t, fragmentResource,
				includeFragment(t, "frag.xml"), entry{"items/frag.xml", tc.fragment})...), "")
			f := fileResult(t, res, "items/frag.xml")
			if f.Valid != tc.valid || res.Valid != tc.valid || f.Skipped {
				t.Fatalf("got %+v, package %+v", f, res)
			}
			if tc.valid && (f.Schema != qti.SchemaFragment || len(f.Errors) != 0) {
				t.Fatalf("got %+v", f)
			}
			if !tc.valid && (len(f.Errors) == 0 || f.Errors[0].Code != tc.code) {
				t.Fatalf("want %s, got %+v", tc.code, f.Errors)
			}
			if item := fileResult(t, res, "items/item-1.xml"); !item.Valid {
				t.Fatalf("item: %+v", item)
			}
		})
	}
}

// An item may list the fragment it includes among its own files; the
// fragment resource still says what the file is.
func TestPackageFragmentListedByItem(t *testing.T) {
	entries := withResources(t, fragmentResource, includeFragment(t, "frag.xml"),
		entry{"items/frag.xml", `<div xmlns="` + qti.NamespaceASI + `"><p>Shared text</p></div>`})
	entries[0].data = strings.Replace(entries[0].data, `<file href="items/item-1.xml"/>`,
		`<file href="items/item-1.xml"/><file href="items/frag.xml"/>`, 1)
	res := validatePackageWith(t, makeZip(t, entries...), "")
	if f := fileResult(t, res, "items/frag.xml"); !res.Valid || !f.Valid || f.Schema != qti.SchemaFragment {
		t.Fatalf("got %+v, package %+v", f, res)
	}
}

// An outcomes resource holds a qti-outcome-declaration, a root element of
// the QTI schema.
func TestPackageOutcomesResource(t *testing.T) {
	outcome := `<qti-outcome-declaration xmlns="` + qti.NamespaceASI + `" identifier="SCORE" cardinality="single" base-type="float">` +
		`<qti-default-value><qti-value>0.0</qti-value></qti-default-value></qti-outcome-declaration>`
	res := validatePackageWith(t, makeZip(t, withResources(t,
		`<resource identifier="out" type="imsqti_outcomes_xmlv3p0" href="outcome.xml"><file href="outcome.xml"/></resource>`,
		entry{"outcome.xml", outcome})...), "")
	if f := fileResult(t, res, "outcome.xml"); !res.Valid || !f.Valid || f.Schema != "qti-outcome-declaration" {
		t.Fatalf("got %+v, package %+v", f, res)
	}
	invalid := strings.Replace(outcome, `base-type="float"`, `base-type="real"`, 1)
	res = validatePackageWith(t, makeZip(t, withResources(t,
		`<resource identifier="out" type="imsqti_outcomes_xmlv3p0" href="outcome.xml"><file href="outcome.xml"/></resource>`,
		entry{"outcome.xml", invalid})...), "")
	if f := fileResult(t, res, "outcome.xml"); res.Valid || f.Valid || f.Errors[0].Code != qti.CodeValidation {
		t.Fatalf("got %+v", f)
	}
}

// On its own, an outcome declaration is a QTI document too.
func TestOutcomeDeclarationDocument(t *testing.T) {
	doc := `<qti-outcome-declaration xmlns="` + qti.NamespaceASI + `" identifier="SCORE" cardinality="single" base-type="float"/>`
	res := testValidator(t).ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc)})
	if !res.Valid || res.Schema != "qti-outcome-declaration" || len(res.Errors) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("got %+v", res)
	}
}

// The manifest of a QTI 3 package must be a QTI 3 content package manifest.
// One in the namespace of plain IMS Content Packaging, or another
// document, makes the package invalid, and the files are not reported as
// unlisted one by one.
func TestPackageManifestNotQTI3(t *testing.T) {
	for name, change := range map[string]func(string) string{
		"generic namespace": func(m string) string {
			return strings.Replace(m, qti.NamespaceManifest, "http://www.imsglobal.org/xsd/imscp_v1p1", 1)
		},
		"an item": func(string) string { return readTestdata(t, "package/item-1.xml") },
	} {
		t.Run(name, func(t *testing.T) {
			entries := packageEntries(t)
			entries[0].data = change(entries[0].data)
			res := validatePackageWith(t, makeZip(t, entries...), "")
			f := fileResult(t, res, qti.ManifestName)
			if res.Valid || f.Valid || f.Skipped {
				t.Fatalf("got %+v, package %+v", f, res)
			}
			last := f.Errors[len(f.Errors)-1]
			if last.Code != qti.CodeUnsupportedDocument || !strings.Contains(last.Message, "not a QTI 3 content package manifest") {
				t.Fatalf("got %+v", f.Errors)
			}
			if len(res.Errors) != 0 || len(res.Warnings) != 0 {
				t.Fatalf("package findings: %+v %+v", res.Errors, res.Warnings)
			}
			for _, other := range res.Files {
				if other.File != qti.ManifestName && (len(other.Warnings) != 0 || len(other.Errors) != 0) {
					t.Fatalf("%s: %+v", other.File, other)
				}
			}
		})
	}
}

// An href with a ":" in a later path segment is a path in the package, to
// which xml:base applies; one that starts with "/" is not in the package.
func TestPackageXMLBaseHrefs(t *testing.T) {
	entries := packageEntries(t)
	entries[0].data = strings.Replace(entries[0].data, "  </resources>",
		`<resource identifier="media" type="webcontent" href="img/a:b.png" xml:base="media/"><file href="img/a:b.png"/></resource>
  </resources>`, 1)
	entries = append(entries, entry{"media/img/a:b.png", "not really a PNG"})
	res := validatePackageWith(t, makeZip(t, entries...), "")
	if m := fileResult(t, res, qti.ManifestName); !res.Valid || len(m.Errors) != 0 {
		t.Fatalf("got %+v, package %+v", m, res)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w.Message, "a:b.png") {
			t.Fatalf("listed file reported as unlisted: %+v", w)
		}
	}

	entries = packageEntries(t)
	entries[0].data = strings.Replace(entries[0].data, "  </resources>",
		`<resource identifier="media" type="webcontent" href="/picture.png" xml:base="items/media/"><file href="picture.png"/></resource>
  </resources>`, 1)
	res = validatePackageWith(t, makeZip(t, entries...), "")
	if m := fileResult(t, res, qti.ManifestName); res.Valid || len(m.Errors) != 1 || m.Errors[0].Code != qti.CodeReference {
		t.Fatalf("got %+v", m)
	}
}

// xml:base applies when deciding which files must be QTI documents, as it
// does for the references.
func TestPackageXMLBaseQTIResource(t *testing.T) {
	entries := packageEntries(t)
	entries[0].data = strings.Replace(entries[0].data,
		`<resource identifier="item-1" type="imsqti_item_xmlv3p0" href="items/item-1.xml">
      <file href="items/item-1.xml"/>`,
		`<resource identifier="item-1" type="imsqti_item_xmlv3p0" href="item-1.xml" xml:base="items/">
      <file href="item-1.xml"/>`, 1)
	res := validatePackageWith(t, makeZip(t, entries...), "")
	if !res.Valid {
		t.Fatalf("valid package: %+v", res)
	}
	entries[2].data = `<assessmentItem xmlns="http://www.imsglobal.org/xsd/imsqti_v2p1" identifier="x"/>`
	res = validatePackageWith(t, makeZip(t, entries...), "")
	f := fileResult(t, res, "items/item-1.xml")
	if res.Valid || f.Skipped || f.Valid || f.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Fatalf("got %+v", f)
	}
}

// A file that could not be read gets its own error; the documents that
// refer to it get none about its type.
func TestPackageReferenceToUnreadableFile(t *testing.T) {
	entries := packageEntries(t)
	entries[2].data = strings.Replace(entries[2].data, "<qti-item-body>", "<qti-item-body><!--"+strings.Repeat("x", 4000)+"-->", 1)
	res := validatePackage(t, makeZip(t, entries...), qti.PackageLimits{MaxFileSize: 3000})
	if f := fileResult(t, res, "items/item-1.xml"); f.Valid || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeTooLarge {
		t.Fatalf("item: %+v", f)
	}
	if f := fileResult(t, res, "test.xml"); !f.Valid || len(f.Errors) != 0 {
		t.Fatalf("test: %+v", f)
	}
}

// An href with a "%" that does not start an escape is reported as an
// invalid URI reference, not as a file outside the package.
func TestPackageInvalidPercentEscape(t *testing.T) {
	entries := packageEntries(t)
	entries[0].data = strings.Replace(entries[0].data, `<file href="items/item-1.xml"/>`,
		`<file href="items/item-1.xml"/><file href="items/100%.png"/>`, 1)
	entries = append(entries, entry{"items/100%.png", "x"})
	res := validatePackageWith(t, makeZip(t, entries...), "")
	f := fileResult(t, res, qti.ManifestName)
	var refs []qti.Finding
	for _, e := range f.Errors {
		if e.Code == qti.CodeReference {
			refs = append(refs, e)
		}
	}
	if res.Valid || len(refs) != 1 || !strings.Contains(refs[0].Message, "not a valid URI reference") || strings.Contains(refs[0].Message, "outside") {
		t.Fatalf("got %+v", f.Errors)
	}
	for _, w := range res.Warnings {
		if w.File == "items/100%.png" {
			t.Fatalf("listed, if not correctly, but reported as unlisted: %+v", w)
		}
	}
}
