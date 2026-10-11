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
		// The parts of the other interactions, of declarations and of an
		// item reference.
		{"associable choice", `<qti-simple-associable-choice` + asi + ` identifier="A" match-max="1">A</qti-simple-associable-choice>`, true, ""},
		{"associable choice without its attribute", `<qti-simple-associable-choice` + asi + ` identifier="A">A</qti-simple-associable-choice>`, false, "match-max"},
		{
			"match set", `<qti-simple-match-set` + asi + `><qti-simple-associable-choice identifier="A" match-max="1">A</qti-simple-associable-choice>` +
				`</qti-simple-match-set>`, true, "",
		},
		{"match set with invalid content", `<qti-simple-match-set` + asi + `><qti-simple-choice identifier="A">A</qti-simple-choice></qti-simple-match-set>`, false, "qti-simple-choice"},
		{"gap text", `<qti-gap-text` + asi + ` identifier="A" match-max="1">A</qti-gap-text>`, true, ""},
		{"gap text without its attribute", `<qti-gap-text` + asi + ` identifier="A">A</qti-gap-text>`, false, "match-max"},
		{"gap image", `<qti-gap-img` + asi + ` identifier="A" match-max="1"><img src="media/picture.png" alt="a"/></qti-gap-img>`, true, ""},
		{"inline choice", `<qti-inline-choice` + asi + ` identifier="A">A</qti-inline-choice>`, true, ""},
		{"inline choice with invalid content", `<qti-inline-choice` + asi + ` identifier="A"><div>A</div></qti-inline-choice>`, false, "div"},
		{"hotspot choice", `<qti-hotspot-choice` + asi + ` identifier="A" shape="rect" coords="0,0,10,10"/>`, true, ""},
		{"hotspot choice without its attributes", `<qti-hotspot-choice` + asi + ` identifier="A"/>`, false, "shape"},
		{"associable hotspot", `<qti-associable-hotspot` + asi + ` identifier="A" shape="rect" coords="0,0,10,10" match-max="1"/>`, true, ""},
		{"content body", `<qti-content-body` + asi + `><p>Well done</p></qti-content-body>`, true, ""},
		{"content body with invalid content", `<qti-content-body` + asi + `><qti-value>1</qti-value></qti-content-body>`, false, "qti-value"},
		{"value", `<qti-value` + asi + `>1</qti-value>`, true, ""},
		{"value with invalid content", `<qti-value` + asi + `><nonsense/></qti-value>`, false, "child elements"},
		{"map entry", `<qti-map-entry` + asi + ` map-key="A" mapped-value="1"/>`, true, ""},
		{"map entry without its attributes", `<qti-map-entry` + asi + `/>`, false, "map-key"},
		{"area map entry", `<qti-area-map-entry` + asi + ` shape="rect" coords="0,0,10,10" mapped-value="1"/>`, true, ""},
		{"match table", `<qti-match-table` + asi + `><qti-match-table-entry source-value="1" target-value="float"/></qti-match-table>`, true, ""},
		{"match table entry", `<qti-match-table-entry` + asi + ` source-value="1" target-value="float"/>`, true, ""},
		{"match table entry without its attributes", `<qti-match-table-entry` + asi + `/>`, false, "source-value"},
		{"interpolation table", `<qti-interpolation-table` + asi + `><qti-interpolation-table-entry source-value="1" target-value="2"/></qti-interpolation-table>`, true, ""},
		{"interpolation table entry", `<qti-interpolation-table-entry` + asi + ` source-value="1" target-value="2"/>`, true, ""},
		{"weight", `<qti-weight` + asi + ` identifier="W" value="1.0"/>`, true, ""},
		{"weight without its value", `<qti-weight` + asi + ` identifier="W"/>`, false, "value"},
		{"variable mapping", `<qti-variable-mapping` + asi + ` source-identifier="a" target-identifier="b"/>`, true, ""},
		{"template default", `<qti-template-default` + asi + ` template-identifier="T"><qti-null/></qti-template-default>`, true, ""},
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
	f, res := fragmentResult(t, `<qti-card`+asi+`><nonsense/></qti-card>`)
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

// In a fragment an IDREF without its ID is no error: the ID may be in the
// document that includes it. In a whole document it stays one.
func TestFragmentUnresolvedIDRef(t *testing.T) {
	const content = `<p data-catalog-idref="nope">x</p><p data-catalog-idref="zz">y</p>`
	for name, fragment := range map[string]string{
		"a global root":         `<div` + asi + `>` + content + `</div>`,
		"a root in a context":   `<qti-simple-choice` + asi + ` identifier="A" data-catalog-idref="zz"><span data-catalog-idref="nope">x</span></qti-simple-choice>`,
		"on the root, and more": `<p` + asi + ` data-catalog-idref="a">` + strings.Repeat(`<span data-catalog-idref="zz">y</span>`, 5) + `</p>`,
	} {
		f, res := fragmentResult(t, fragment)
		if !f.Valid || !res.Valid || f.Ran != (qti.Checks{Schema: true}) || len(f.Errors) != 0 || len(f.Warnings) != 0 {
			t.Errorf("%s: got %+v", name, f)
		}
	}
	// Other errors of the fragment stay, each IDREF or not.
	f, _ := fragmentResult(t, `<div`+asi+`>`+content+`<bogus/></div>`)
	if f.Valid || len(f.Errors) != 1 || !strings.Contains(f.Errors[0].Message, "bogus") {
		t.Errorf("with another error: %+v", f.Errors)
	}

	// The same content inside a whole item.
	item := strings.Replace(readTestdata(t, "package/item-1.xml"), "<qti-item-body>", "<qti-item-body><div>"+content+"</div>", 1)
	res := validateString(t, item, "")
	if res.Valid || len(res.Errors) != 2 {
		t.Fatalf("whole item: %+v", res)
	}
	for i, id := range []string{"nope", "zz"} {
		if e := res.Errors[i]; e.Code != qti.CodeValidation || !strings.Contains(e.Message, "IDREF") || !strings.HasSuffix(e.Message, id) {
			t.Errorf("whole item: %+v", e)
		}
	}
}

// The IDREFs that are no error in a fragment do not take the room of its
// errors under MAX_ERRORS.
func TestFragmentUnresolvedIDRefsLeaveRoomForErrors(t *testing.T) {
	idrefs := strings.Repeat(`<span data-catalog-idref="zz">y</span>`, 5)
	const bogus = `<span><bogus/></span>`
	for _, tc := range []struct {
		name, fragment string
		errors         int
	}{
		{"only IDREFs", `<div` + asi + `>` + idrefs + `</div>`, 0},
		{"IDREFs before errors", `<div` + asi + `>` + idrefs + bogus + bogus + `</div>`, 2},
		{"IDREFs before more errors than the limit", `<div` + asi + `>` + idrefs + bogus + bogus + bogus + `</div>`, 2},
		{"in a context", `<qti-prompt` + asi + `>` + idrefs + bogus + bogus + `</qti-prompt>`, 2},
	} {
		data := makeZip(t, withResources(t, fragmentResource, includeFragment(t, "frag.xml"), entry{"items/frag.xml", tc.fragment})...)
		res := testValidator(t).WithLimits(qti.Limits{MaxErrors: 2}).ValidatePackage(context.Background(),
			app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
		f := fileResult(t, res, "items/frag.xml")
		if len(f.Errors) != tc.errors || f.Valid != (tc.errors == 0) {
			t.Errorf("%s: got %+v", tc.name, f)
		}
		for _, e := range f.Errors {
			if e.Code != qti.CodeValidation || strings.Contains(e.Message, "IDREF") {
				t.Errorf("%s: %+v", tc.name, e)
			}
		}
	}
}

// Every element the schema declares is either validated as the root of a
// fragment or reported as not checked; none passes silently. The elements
// that are not checked are parts of catalogs, of companion materials and
// of portable custom interactions, and a few others.
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

const uncheckedRoots = "param qti-adaptive-engine-ref qti-adaptive-settings-ref qti-calculator qti-calculator-info " +
	"qti-calculator-type qti-card qti-card-entry qti-catalog qti-context-variable qti-description " +
	"qti-digital-material qti-file-href qti-html-content qti-increment-si qti-increment-us " +
	"qti-interaction-markup qti-interaction-module qti-interaction-modules qti-label qti-major-increment " +
	"qti-metadata-ref qti-minimum-length qti-minor-increment qti-physical-material " +
	"qti-position-object-interaction qti-protractor qti-resource-icon qti-rule qti-rule-system-si " +
	"qti-rule-system-us qti-template-variable qti-usagedata-ref"

const sharedDiv = `<div` + asi + `><p>Shared text</p></div>`

// listAsItemFile lists a file, items/frag.xml unless one is named, among the
// files of the item's resource.
func listAsItemFile(entries []entry, file ...string) []entry {
	name := "items/frag.xml"
	if len(file) > 0 {
		name = file[0]
	}
	entries[0].data = strings.Replace(entries[0].data, `<file href="items/item-1.xml"/>`,
		`<file href="items/item-1.xml"/><file href="`+name+`"/>`, 1)
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
// with or without a fragment resource. Included XML outside the QTI
// namespace is skipped, whether the manifest does not list it or lists it
// as a file of the item; only a fragment resource makes it an error. A file
// included as text stays skipped too.
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
	for name, entries := range map[string][]entry{
		"not listed":         withResources(t, "", includeFragment(t, "../shared/formula.xml"), entry{"shared/formula.xml", math}),
		"listed by the item": listAsItemFile(withResources(t, "", includeFragment(t, "formula.xml"), entry{"items/formula.xml", math}), "items/formula.xml"),
	} {
		res := validatePackageWith(t, makeZip(t, entries...), "")
		file := entries[len(entries)-1].name
		f := fileResult(t, res, file)
		if !res.Valid || !f.Valid || !f.Skipped || f.Ran != (qti.Checks{}) {
			t.Fatalf("MathML, %s: %+v, package %+v", name, f, res)
		}
		r := report.ForPackage(res, report.Meta{InputName: "p.zip", Now: time.Now()})
		if len(r.Fatals) != 0 || len(r.Errors) != 0 || !slices.ContainsFunc(r.NotRun, func(it report.Item) bool {
			return it.Code == "skipped" && it.Location.Resource == "/"+file
		}) {
			t.Fatalf("MathML, %s: report %+v", name, r)
		}
	}
	// A fragment resource says it is QTI content, which it is not.
	f, res := fragmentResult(t, math)
	if res.Valid || f.Valid || f.Skipped || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeUnsupportedDocument ||
		!strings.Contains(f.Errors[0].Message, "is not in the QTI namespace") {
		t.Fatalf("MathML as a fragment resource: %+v", f)
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
