package app_test

import (
	"bytes"
	"context"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/adapter/xsdschema"
	"qti3-validator/internal/app"
	"qti3-validator/internal/app/report"
	"qti3-validator/internal/domain/qti"
)

const asi = ` xmlns="` + qti.NamespaceASI + `"`

// fragmentResult validates the test package with a fragment that item-1
// includes and the manifest lists as a fragment resource.
func fragmentResult(t *testing.T, fragment string) (qti.FileResult, qti.PackageResult) {
	t.Helper()
	res := validatePackageWith(t, makeZip(t, withResources(t, fragmentResource,
		includeFragment(t, "frag.xml"), entry{"items/frag.xml", fragment})...), "")
	return fileResult(t, res, "items/frag.xml"), res
}

func notChecked(f qti.FileResult) bool {
	return slices.ContainsFunc(f.Warnings, func(w qti.Finding) bool { return w.Code == qti.CodeNotChecked })
}

// The root of a fragment must be an element of the QTI schema. One the
// schema declares only inside other elements is validated inside an element
// that allows it.
func TestFragmentRoot(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		valid          bool
		message        string // of the first error
	}{
		{"not an element of QTI", `<qti-bogus-element` + asi + `><whatever/><qti-item-body/></qti-bogus-element>`, false, "not an element of QTI 3.0.0"},
		{"response rule with invalid content", `<qti-response-condition` + asi + `><nonsense/></qti-response-condition>`, false, "nonsense"},
		{
			"response rule", `<qti-response-condition` + asi + `><qti-response-if><qti-match><qti-variable identifier="RESPONSE"/>` +
				`<qti-correct identifier="RESPONSE"/></qti-match><qti-set-outcome-value identifier="SCORE">` +
				`<qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value></qti-response-if></qti-response-condition>`, true, "",
		},
		{"outcome rule", `<qti-outcome-condition` + asi + `><qti-outcome-if><qti-null/><qti-exit-test/></qti-outcome-if></qti-outcome-condition>`, true, ""},
		{"outcome rule with invalid content", `<qti-outcome-condition` + asi + `><qti-exit-test/></qti-outcome-condition>`, false, "qti-exit-test"},
		{"template rule", `<qti-set-template-value` + asi + ` identifier="T"><qti-base-value base-type="integer">1</qti-base-value></qti-set-template-value>`, true, ""},
		{"template rule without its attribute", `<qti-set-template-value` + asi + `><qti-null/></qti-set-template-value>`, false, "identifier"},
		{
			"interaction", `<qti-choice-interaction` + asi + ` response-identifier="RESPONSE" max-choices="1">` +
				`<qti-simple-choice identifier="A">A</qti-simple-choice></qti-choice-interaction>`, true, "",
		},
		{"interaction without choices", `<qti-choice-interaction` + asi + ` response-identifier="RESPONSE"/>`, false, "missing required child"},
		// Its response declaration is in the item that includes it.
		{"inline interaction", `<qti-text-entry-interaction` + asi + ` response-identifier="OTHER"/>`, true, ""},
		{"global root with an interaction", `<p` + asi + `>x <qti-text-entry-interaction response-identifier="OTHER"/></p>`, true, ""},
		{"expression", `<qti-sum` + asi + `><qti-variable identifier="A"/><qti-variable identifier="B"/></qti-sum>`, true, ""},
		{"expression with invalid content", `<qti-sum` + asi + `><p>x</p></qti-sum>`, false, "p"},
		{"choice", `<qti-simple-choice` + asi + ` identifier="A">A</qti-simple-choice>`, true, ""},
		{"choice without its identifier", `<qti-simple-choice` + asi + `>A</qti-simple-choice>`, false, "identifier"},
		{"else branch", `<qti-response-else` + asi + `><qti-exit-response/></qti-response-else>`, true, ""},
		{"else branch with invalid content", `<qti-response-else` + asi + `><qti-exit-test/></qti-response-else>`, false, "qti-exit-test"},
		{"item reference", `<qti-assessment-item-ref` + asi + ` identifier="i" href="item-1.xml"/>`, true, ""},
		{"test part", `<qti-test-part` + asi + ` identifier="p" navigation-mode="linear" submission-mode="individual"/>`, false, "missing required child"},
		// Elements of the fragment in no namespace stay there.
		{"prefixed root", `<q:qti-response-condition xmlns:q="` + qti.NamespaceASI + `"><qti-response-if/></q:qti-response-condition>`, false, "qti-response-if"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, res := fragmentResult(t, tc.fragment)
			if f.Valid != tc.valid || res.Valid != tc.valid || f.Skipped || f.Schema != qti.SchemaFragment ||
				f.Ran != (qti.Checks{Schema: true}) || len(f.Warnings) != 0 {
				t.Fatalf("got %+v, package %+v", f, res)
			}
			if tc.valid != (len(f.Errors) == 0) {
				t.Fatalf("errors %+v", f.Errors)
			}
			if !tc.valid {
				e := f.Errors[0]
				root := strings.TrimPrefix(strings.Fields(tc.fragment)[0], "<")
				root = root[strings.Index(root, ":")+1:]
				if e.Code != qti.CodeValidation || !strings.Contains(e.Message, tc.message) || e.Line != 1 ||
					e.Path != "/"+root || strings.Contains(e.Message, "validator-context") {
					t.Fatalf("got %+v", e)
				}
			}
		})
	}
}

// A root that is valid only in a place the validator does not know is not
// checked, and the result says so: a warning, and no schema check counted.
func TestFragmentNotChecked(t *testing.T) {
	f, res := fragmentResult(t, `<qti-value`+asi+`><nonsense/></qti-value>`)
	if !f.Valid || !res.Valid || !notChecked(f) || f.Ran != (qti.Checks{}) || f.Schema != qti.SchemaFragment || len(f.Errors) != 0 {
		t.Fatalf("got %+v", f)
	}
	r := report.ForPackage(res, report.Meta{InputName: "p.zip", Now: time.Now()})
	// The package, and three checks for each of the manifest, the test and
	// the item; for the fragment only reading it. It is not among the
	// valid documents.
	w := r.Warnings[len(r.Warnings)-1]
	if r.Summary.Outcome != report.OutcomeNameWarning || r.Summary.TotalRun != 11 || r.Summary.Valid != 3 || r.Summary.NotRun != 1 ||
		w.Code != "not_checked" || w.Location.Resource != "/items/frag.xml" || r.NotRun[0].Location.Resource != "/items/frag.xml" {
		t.Fatalf("report %+v", r)
	}
}

// Findings in a fragment that is validated inside another element are
// located in the fragment as it is: the same line, column and path as when
// its root were a global element.
func TestFragmentFindingLocation(t *testing.T) {
	const prolog = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!-- shared -->\n  "
	const rule, div = `<qti-response-condition` + asi + `>`, `<div` + asi + `>`
	inRule, _ := fragmentResult(t, prolog+rule+`<nonsense/>`+"\n"+`<qti-response-if x="1"><qti-null/></qti-response-if></qti-response-condition>`)
	inDiv, _ := fragmentResult(t, prolog+div+`<nonsense/>`+"\n"+`<p>text</p></div>`)
	if len(inRule.Errors) != 2 || len(inDiv.Errors) != 1 {
		t.Fatalf("got %+v and %+v", inRule.Errors, inDiv.Errors)
	}
	// The first error is on the line of the root, after its start tag,
	// which is indented by two spaces; the second on the next line.
	if a, b := inRule.Errors[0], inDiv.Errors[0]; a.Line != 3 || a.Column != 3+len(rule) || a.Path != "/qti-response-condition" ||
		b.Line != 3 || b.Column != 3+len(div) || b.Path != "/div" {
		t.Errorf("first: %+v and %+v", a, b)
	}
	if e := inRule.Errors[1]; e.Line != 4 || e.Column != 1 || e.Path != "/qti-response-condition/qti-response-if" {
		t.Errorf("second: %+v", e)
	}
	// An error on the root itself.
	onRoot, _ := fragmentResult(t, prolog+`<qti-set-outcome-value`+asi+`><qti-null/></qti-set-outcome-value>`)
	if e := onRoot.Errors[0]; len(onRoot.Errors) != 1 || e.Line != 3 || e.Column != 3 || e.Path != "/qti-set-outcome-value" {
		t.Errorf("on the root: %+v", onRoot.Errors)
	}
}

// Every element the schema declares is either validated as the root of a
// fragment or reported as not checked; none passes silently. The elements
// that are not checked are parts of interactions, declarations, catalogs
// and companion materials.
func TestFragmentEveryElement(t *testing.T) {
	for _, version := range qti.Versions() {
		elements, err := xsdschema.DeclaredElements(xsdschema.Resolver{Store: schemastore.Embedded()}, version.ASI)
		if err != nil {
			t.Fatal(err)
		}
		if elements.Namespace != qti.NamespaceASI || !elements.Global["div"] || !elements.Local["qti-response-condition"] ||
			elements.Global["qti-response-condition"] || elements.Declares("qti-bogus-element") {
			t.Fatalf("%s: elements %+v", version.Name, elements)
		}
		var unchecked []string
		for name := range elements.Local {
			res := validatePackageWith(t, makeZip(t, withResources(t, fragmentResource,
				includeFragment(t, "frag.xml"), entry{"items/frag.xml", `<` + name + asi + `><nonsense/></` + name + `>`})...), version.Name)
			f := fileResult(t, res, "items/frag.xml")
			switch {
			case notChecked(f) && f.Valid && f.Ran == (qti.Checks{}):
				unchecked = append(unchecked, name)
			case !f.Valid && f.Ran.Schema && len(f.Errors) > 0:
			default:
				t.Errorf("%s: %s passed without a check: %+v", version.Name, name, f)
			}
		}
		sort.Strings(unchecked)
		if got := strings.Join(unchecked, " "); got != uncheckedRoots {
			t.Errorf("%s: not checked:\n%s\nwant:\n%s", version.Name, got, uncheckedRoots)
		}
	}
}

const uncheckedRoots = "param qti-adaptive-engine-ref qti-adaptive-settings-ref qti-area-map-entry qti-associable-hotspot " +
	"qti-calculator qti-calculator-info qti-calculator-type qti-card qti-card-entry qti-catalog qti-content-body " +
	"qti-context-variable qti-description qti-digital-material qti-file-href qti-gap-img qti-gap-text qti-hotspot-choice " +
	"qti-html-content qti-increment-si qti-increment-us qti-inline-choice qti-interaction-markup qti-interaction-module " +
	"qti-interaction-modules qti-interpolation-table qti-interpolation-table-entry qti-label qti-major-increment " +
	"qti-map-entry qti-match-table qti-match-table-entry qti-metadata-ref qti-minimum-length qti-minor-increment " +
	"qti-physical-material qti-position-object-interaction qti-protractor qti-resource-icon qti-rule qti-rule-system-si " +
	"qti-rule-system-us qti-simple-associable-choice qti-simple-match-set qti-template-default qti-template-variable " +
	"qti-usagedata-ref qti-value qti-variable-mapping qti-weight"

const sharedDiv = `<div` + asi + `><p>Shared text</p></div>`

// listAsItemFile lists items/frag.xml among the files of the item's
// resource.
func listAsItemFile(entries []entry) []entry {
	entries[0].data = strings.Replace(entries[0].data, `<file href="items/item-1.xml"/>`,
		`<file href="items/item-1.xml"/><file href="items/frag.xml"/>`, 1)
	return entries
}

// A file that a QTI document includes with XInclude is a fragment, also
// when the manifest has no fragment resource for it: listed as a file of
// the item that includes it, or not listed at all.
func TestPackageIncludedFileIsFragment(t *testing.T) {
	for _, tc := range []struct {
		name, fragment string
		list           bool
		valid          bool
	}{
		{"listed by the item", sharedDiv, true, true},
		{"listed by the item, invalid", `<div` + asi + `><bogus/></div>`, true, false},
		{"listed by the item, a response rule", `<qti-response-condition` + asi + `><nonsense/></qti-response-condition>`, true, false},
		{"not listed", sharedDiv, false, true},
		{"not listed, invalid", `<div` + asi + `><bogus/></div>`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries := withResources(t, "", includeFragment(t, "frag.xml"), entry{"items/frag.xml", tc.fragment})
			if tc.list {
				entries = listAsItemFile(entries)
			}
			// Whether the fragment comes after the item in the package, or before it.
			for _, order := range [][]entry{entries, append([]entry{entries[0], entries[len(entries)-1]}, entries[1:len(entries)-1]...)} {
				res := validatePackageWith(t, makeZip(t, order...), "")
				f := fileResult(t, res, "items/frag.xml")
				if f.Valid != tc.valid || f.Skipped || f.Schema != qti.SchemaFragment || !f.Ran.Schema {
					t.Fatalf("got %+v", f)
				}
				if !tc.valid && (res.Valid || f.Errors[0].Code != qti.CodeValidation) {
					t.Fatalf("got %+v", f.Errors)
				}
				if tc.valid && tc.list && (!res.Valid || len(f.Warnings) != 0) {
					t.Fatalf("got %+v, package %+v", f, res)
				}
			}
		})
	}
}

// Without an XInclude, a file of an item resource that is not a QTI
// document stays an error, and an unlisted one stays skipped.
func TestPackageNotIncludedFileIsNoFragment(t *testing.T) {
	entries := listAsItemFile(withResources(t, "", entry{"items/frag.xml", sharedDiv}))
	res := validatePackageWith(t, makeZip(t, entries...), "")
	if f := fileResult(t, res, "items/frag.xml"); res.Valid || f.Valid || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Fatalf("got %+v", f)
	}
	res = validatePackageWith(t, makeZip(t, withResources(t, "", entry{"items/frag.xml", sharedDiv})...), "")
	if f := fileResult(t, res, "items/frag.xml"); !f.Valid || !f.Skipped {
		t.Fatalf("got %+v", f)
	}
}

// An included file that is a QTI document is validated as that document,
// with or without a fragment resource; included XML outside the QTI
// namespace that the manifest does not list, and a file included as text,
// stay skipped.
func TestPackageIncludedDocumentAndOtherXML(t *testing.T) {
	item2 := strings.Replace(readTestdata(t, "package/item-1.xml"), `identifier="item-1"`, `identifier="item-2"`, 1)
	for _, resources := range []string{
		`<resource identifier="item-2" type="imsqti_item_xmlv3p0" href="items/frag.xml"><file href="items/frag.xml"/></resource>`,
		fragmentResource,
	} {
		res := validatePackageWith(t, makeZip(t, withResources(t, resources, includeFragment(t, "frag.xml"), entry{"items/frag.xml", item2})...), "")
		if f := fileResult(t, res, "items/frag.xml"); !f.Valid || f.Schema != "qti-assessment-item" || f.Ran != (qti.Checks{Schema: true, Rules: true}) {
			t.Fatalf("got %+v", f)
		}
	}

	const math = `<math xmlns="http://www.w3.org/1998/Math/MathML"><mi>x</mi></math>`
	res := validatePackageWith(t, makeZip(t, withResources(t, "", includeFragment(t, "../shared/formula.xml"), entry{"shared/formula.xml", math})...), "")
	if f := fileResult(t, res, "shared/formula.xml"); !f.Valid || !f.Skipped {
		t.Fatalf("MathML: %+v", f)
	}
	// Listed as a file of a QTI resource it must be QTI content.
	res = validatePackageWith(t, makeZip(t, listAsItemFile(withResources(t, "", includeFragment(t, "frag.xml"), entry{"items/frag.xml", math}))...), "")
	if f := fileResult(t, res, "items/frag.xml"); f.Valid || f.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Fatalf("listed MathML: %+v", f)
	}

	text := includeFragment(t, "frag.xml")
	text.data = strings.Replace(text.data, `href="frag.xml"`, `href="frag.xml" parse="text"`, 1)
	res = validatePackageWith(t, makeZip(t, withResources(t, "", text, entry{"items/frag.xml", `<div` + asi + `><bogus/></div>`})...), "")
	if f := fileResult(t, res, "items/frag.xml"); !f.Valid || !f.Skipped {
		t.Fatalf("included as text: %+v", f)
	}
}

// A manifest that is another QTI document is reported as not a manifest
// only: the checks of the document it is instead are not in its result, and
// the report counts and lists them as not run.
func TestPackageManifestIsAnotherDocument(t *testing.T) {
	entries := packageEntries(t)
	entries[0].data = readTestdata(t, "package/item-1.xml")
	res := validatePackageWith(t, makeZip(t, entries...), "")
	f := fileResult(t, res, qti.ManifestName)
	if f.Valid || f.Schema != "" || f.Ran != (qti.Checks{}) || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Fatalf("got %+v", f)
	}
	r := report.ForPackage(res, report.Meta{InputName: "p.zip", Now: time.Now()})
	// The package, reading the manifest, and three checks for each of the
	// test and the item.
	if r.Summary.TotalRun != 8 || r.Summary.Fatals != 1 || r.Summary.NotRun != 1 ||
		r.NotRun[0].Message != "Not run: the document is not a supported document type." {
		t.Fatalf("report %+v", r)
	}
}

// A manifest of a document type that a mounted validator adds is that
// validator's to judge.
func TestMountedManifest(t *testing.T) {
	v := testMountedValidator(t)
	for doc, valid := range map[string]bool{
		`<meta xmlns="urn:example:meta"><level>basic</level></meta>`: true,
		`<meta xmlns="urn:example:meta"><level>none</level></meta>`:  false,
	} {
		entries := packageEntries(t)
		entries[0].data = doc
		data := makeZip(t, entries...)
		res := v.ValidatePackage(context.Background(), app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
		f := fileResult(t, res, qti.ManifestName)
		if f.Valid != valid || f.Schema != "meta" || !f.Ran.Schema ||
			slices.ContainsFunc(f.Errors, func(e qti.Finding) bool { return e.Code == qti.CodeUnsupportedDocument }) {
			t.Fatalf("%s: got %+v", doc, f)
		}
	}
}
