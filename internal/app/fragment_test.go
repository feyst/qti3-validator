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
		// Roots that are another type in another element. Item-1 includes
		// the fragment in its qti-item-body, so a rubric block, a feedback
		// block and a template block are those of an item body; nothing may
		// include a qti-content-body there, so that one is valid when it is
		// the content of any element that has one (TestFragmentWhereIncluded).
		{"rubric block of an item", `<qti-rubric-block` + asi + ` view="candidate" use="instructions"><qti-content-body><p>Read</p></qti-content-body></qti-rubric-block>`, true, ""},
		{"rubric block of a section, in an item", `<qti-rubric-block` + asi + ` view="candidate"><qti-content-body><p>Read</p></qti-content-body></qti-rubric-block>`, false, "missing required attribute use"},
		{
			"content body with a feedback block", `<qti-content-body` + asi + `><p>Well done</p><qti-feedback-block outcome-identifier="FB" identifier="more">` +
				`<qti-content-body><p>More</p></qti-content-body></qti-feedback-block></qti-content-body>`, true, "",
		},
		{
			"feedback block with an interaction", `<qti-feedback-block` + asi + ` outcome-identifier="FB" identifier="f"><qti-content-body>` +
				`<qti-extended-text-interaction response-identifier="RESPONSE"/></qti-content-body></qti-feedback-block>`, true, "",
		},
		{
			"template block with an expression", `<qti-template-block` + asi + ` template-identifier="T" identifier="t"><qti-content-body>` +
				`<qti-math-constant name="pi"/></qti-content-body></qti-template-block>`, false, "qti-math-constant",
		},
		{"template inline", `<qti-template-inline` + asi + ` template-identifier="T" identifier="t">text</qti-template-inline>`, true, ""},
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
					!strings.HasPrefix(e.Path+"/", "/"+root+"/") || strings.Contains(e.Message, "validator-context") {
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
		var unchecked, severalPlaces []string
		for name := range elements.Local {
			// Included by an item, and as a fragment resource that no
			// document includes: checked, or not, either way.
			var results []qti.FileResult
			for _, files := range [][]entry{{includeFragment(t, "frag.xml")}, nil} {
				files = append(files, entry{"items/frag.xml", `<` + name + asi + `><nonsense/></` + name + `>`})
				res := validatePackageWith(t, makeZip(t, withResources(t, fragmentResource, files...)...), version.Name)
				results = append(results, fileResult(t, res, "items/frag.xml"))
			}
			for _, f := range results {
				switch {
				case notChecked(f) && f.Valid && f.Ran == (qti.Checks{}):
				case !f.Valid && f.Ran.Schema && len(f.Errors) > 0:
				default:
					t.Errorf("%s: %s passed without a check: %+v", version.Name, name, f)
				}
			}
			if notChecked(results[0]) != notChecked(results[1]) {
				t.Errorf("%s: %s is checked in one package and not in the other: %+v", version.Name, name, results)
			}
			if notChecked(results[0]) {
				unchecked = append(unchecked, name)
			}
			places := app.FragmentContextPaths(testValidator(t), version.Name, name)
			if len(places) > 1 {
				severalPlaces = append(severalPlaces, name)
			}
			// A root that is another type in another element has a place
			// for each of its types, or is not checked at all.
			if want, ok := manyTypedRoots[name]; elements.ManyTyped[name] != ok && !notChecked(results[0]) || ok && !slices.Equal(places, want) {
				t.Errorf("%s: %s is validated in\n%q\nwant\n%q", version.Name, name, places, want)
			}
		}
		sort.Strings(unchecked)
		if got := strings.Join(unchecked, " "); got != uncheckedRoots {
			t.Errorf("%s: not checked:\n%s\nwant:\n%s", version.Name, got, uncheckedRoots)
		}
		var manyTyped []string
		for name := range elements.ManyTyped {
			manyTyped = append(manyTyped, name)
		}
		sort.Strings(manyTyped)
		if got := strings.Join(manyTyped, " "); got != "qti-content-body qti-feedback-block qti-file-href qti-major-increment "+
			"qti-minor-increment qti-rubric-block qti-template-block qti-template-inline" {
			t.Errorf("%s: declared with more than one type: %s", version.Name, got)
		}
		// docs/report.md says how many roots may stand in several places.
		if len(severalPlaces) != 42 {
			sort.Strings(severalPlaces)
			t.Errorf("%s: %d roots are validated in more than one place: %s", version.Name, len(severalPlaces), strings.Join(severalPlaces, " "))
		}
	}
}

// manyTypedRoots are the roots the schema declares with more than one type,
// and that are checked: the elements each is validated in, one for each of
// its types, and for a qti-rubric-block of a test the three that have it.
var manyTypedRoots = map[string][]string{
	"qti-rubric-block": {
		"/qti-assessment-item/qti-item-body", "/qti-assessment-section", "/qti-assessment-test", "/qti-assessment-test/qti-test-part",
	},
	"qti-content-body": {
		"/qti-assessment-item/qti-modal-feedback",
		"/qti-assessment-item/qti-item-body/qti-feedback-block",
		"/qti-assessment-item/qti-item-body/qti-rubric-block",
		"/qti-assessment-item/qti-item-body/qti-template-block",
		"/qti-assessment-item/qti-item-body/qti-rubric-block/qti-content-body/qti-template-block",
		"/qti-assessment-test/qti-test-feedback",
		"/qti-assessment-section/qti-rubric-block",
	},
	"qti-feedback-block": {
		"/qti-assessment-item/qti-item-body", "/div", "/qti-assessment-item/qti-item-body/qti-template-block/qti-content-body",
	},
	"qti-template-block": {
		"/qti-assessment-item/qti-item-body", "/div",
		"/qti-assessment-item/qti-item-body/qti-rubric-block/qti-content-body",
		"/qti-assessment-item/qti-item-body/qti-template-block/qti-content-body",
	},
	"qti-template-inline": {
		"/div", "/p",
		"/qti-assessment-item/qti-item-body/qti-rubric-block/qti-content-body",
		"/qti-assessment-item/qti-item-body/qti-template-block/qti-content-body",
	},
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

// includePlaces are places where a document of the test package includes
// items/frag.xml: the file, the text of it the XInclude is put at, and what
// replaces that text, with INCLUDE for the XInclude.
var includePlaces = map[string][3]string{
	"item body":          {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body>INCLUDE`},
	"div in item body":   {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><div>INCLUDE</div>`},
	"li in item body":    {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><ul><li>INCLUDE</li></ul>`},
	"item rubric body":   {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><qti-rubric-block view="candidate" use="instructions"><qti-content-body>INCLUDE</qti-content-body></qti-rubric-block>`},
	"item rubric":        {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><qti-rubric-block view="candidate" use="instructions">INCLUDE</qti-rubric-block>`},
	"template body":      {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><qti-template-block template-identifier="T" identifier="t"><qti-content-body>INCLUDE</qti-content-body></qti-template-block>`},
	"feedback block":     {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><qti-feedback-block outcome-identifier="FB" identifier="f">INCLUDE</qti-feedback-block>`},
	"feedback body":      {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><qti-feedback-block outcome-identifier="FB" identifier="f"><qti-content-body>INCLUDE</qti-content-body></qti-feedback-block>`},
	"modal feedback":     {"items/item-1.xml", `</qti-assessment-item>`, `<qti-modal-feedback outcome-identifier="FB" identifier="f" show-hide="show">INCLUDE</qti-modal-feedback></qti-assessment-item>`},
	"section":            {"test.xml", `visible="true">`, `visible="true">INCLUDE`},
	"section rubric":     {"test.xml", `visible="true">`, `visible="true"><qti-rubric-block view="candidate">INCLUDE</qti-rubric-block>`},
	"test part":          {"test.xml", `submission-mode="individual">`, `submission-mode="individual">INCLUDE`},
	"another fragment":   {"items/other.xml", ``, `<qti-rubric-block` + asi + ` view="candidate">INCLUDE</qti-rubric-block>`},
	"math in item body":  {"items/item-1.xml", `<qti-item-body>`, `<qti-item-body><m:math xmlns:m="http://www.w3.org/1998/Math/MathML">INCLUDE</m:math>`},
	"section, by a path": {"test.xml", `visible="true">`, `visible="true"><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="./items/../items/frag.xml"/>`},
}

// fragmentIn validates the test package with the fragment items/frag.xml,
// which its documents include in the given places; in none, the manifest
// lists it as a fragment resource that no document includes. It returns the
// result of the fragment.
func fragmentIn(t *testing.T, fragment string, places ...string) qti.FileResult {
	t.Helper()
	resources := ""
	if len(places) == 0 {
		resources = fragmentResource
	}
	entries := withResources(t, resources, entry{"items/frag.xml", fragment})
	for _, place := range places {
		p, ok := includePlaces[place]
		if !ok {
			t.Fatalf("no place %q", place)
		}
		href := "frag.xml"
		if p[0] == "test.xml" {
			href = "items/frag.xml"
		}
		include := strings.ReplaceAll(p[2], "INCLUDE", `<xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="`+href+`"/>`)
		i := slices.IndexFunc(entries, func(e entry) bool { return e.name == p[0] })
		switch {
		case i < 0:
			entries = append(entries, entry{p[0], include})
		case !strings.Contains(entries[i].data, p[1]):
			t.Fatalf("%s has no %s", p[0], p[1])
		default:
			entries[i].data = strings.Replace(entries[i].data, p[1], include, 1)
		}
	}
	return fileResult(t, validatePackageWith(t, makeZip(t, entries...), ""), "items/frag.xml")
}

// A root that is another type in another element is validated as the type
// it has where the documents of the package include it: it must be valid in
// each of those places. Where that is not known, as for a fragment resource
// that no document includes, it is an error only when it is valid nowhere.
func TestFragmentWhereIncluded(t *testing.T) {
	body := func(content string) string { return `<qti-content-body>` + content + `</qti-content-body>` }
	// The rubric block of a section needs no use attribute, that of an item
	// does; the one of an item may hold a qti-printed-variable.
	sectionRubric := `<qti-rubric-block` + asi + ` view="candidate">` + body(`<p>Read carefully</p>`) + `</qti-rubric-block>`
	itemRubric := `<qti-rubric-block` + asi + ` view="candidate" use="instructions">` + body(`<qti-printed-variable identifier="T"/>`) + `</qti-rubric-block>`
	noRubric := `<qti-rubric-block` + asi + ` view="candidate" use="instructions">` + body(`<qti-value>1</qti-value>`) + `</qti-rubric-block>`
	feedback := func(ns, content string) string {
		return `<qti-feedback-block` + ns + ` outcome-identifier="FB" identifier="f">` + body(content) + `</qti-feedback-block>`
	}
	// The content of a feedback block may hold another feedback block, that
	// of modal feedback and of a rubric block may not.
	contentBody := `<qti-content-body` + asi + `><p>Well done</p>` + feedback("", `<p>More</p>`) + `</qti-content-body>`
	const interaction = `<qti-extended-text-interaction response-identifier="RESPONSE"/>`
	template := func(content string) string {
		return `<qti-template-block` + asi + ` template-identifier="T" identifier="t">` + body(content) + `</qti-template-block>`
	}
	const (
		inItem    = " (checked as a child of qti-item-body, where items/item-1.xml includes the fragment)"
		inSection = " (checked as a child of qti-assessment-section, where test.xml includes the fragment)"
	)
	for _, tc := range []struct {
		name, fragment string
		places         []string
		errors         []string // the path and the message of each
	}{
		{"section rubric, not included", sectionRubric, nil, nil},
		{"section rubric, in a section", sectionRubric, []string{"section"}, nil},
		{"section rubric, in a section by another path", sectionRubric, []string{"section, by a path"}, nil},
		{"section rubric, in a test part", sectionRubric, []string{"test part"}, nil},
		{"section rubric, in an item body", sectionRubric, []string{"item body"}, []string{"/qti-rubric-block missing required attribute use" + inItem}},
		// Valid in the section, not in the item: it must be valid in both.
		{"section rubric, in an item body and a section", sectionRubric, []string{"section", "item body"}, []string{"/qti-rubric-block missing required attribute use" + inItem}},
		{"item rubric, not included", itemRubric, nil, nil},
		{"item rubric, in an item body", itemRubric, []string{"item body"}, nil},
		{"item rubric, in a section", itemRubric, []string{"section"}, []string{"/qti-rubric-block/qti-content-body unexpected child element qti-printed-variable" + inSection}},
		// Valid nowhere: the errors of each place, in the order of the
		// documents that include it.
		{"invalid rubric, in an item body and a section", noRubric, []string{"section", "item body"}, []string{
			"/qti-rubric-block/qti-content-body unexpected child element qti-value" + inItem,
			"/qti-rubric-block/qti-content-body unexpected child element qti-value" + inSection,
		}},
		{"invalid rubric, not included", noRubric, nil, []string{
			"/qti-rubric-block/qti-content-body unexpected child element qti-value (checked as a child of qti-item-body: the fragment " +
				"is valid in none of the 4 places the validator knows for a qti-rubric-block, and has the fewest errors in this one)",
		}},
		// Places the validator has no element for: valid when it is valid
		// in any element. In a document that is a fragment itself, the
		// rubric block around it may be that of an item or of a section.
		{"section rubric, in a list item", sectionRubric, []string{"li in item body"}, nil},
		{"section rubric, in MathML", sectionRubric, []string{"math in item body"}, nil},
		{"content body, in a fragment", contentBody, []string{"another fragment"}, nil},

		{"content body, not included", contentBody, nil, nil},
		{"content body, in a feedback block", contentBody, []string{"feedback block"}, nil},
		{"content body, in modal feedback", contentBody, []string{"modal feedback"}, []string{
			"/qti-content-body unexpected child element qti-feedback-block (checked as a child of qti-modal-feedback, where items/item-1.xml includes the fragment)",
		}},
		{"content body, in an item rubric", contentBody, []string{"item rubric"}, []string{
			"/qti-content-body unexpected child element qti-feedback-block (checked as a child of qti-rubric-block of qti-item-body, where items/item-1.xml includes the fragment)",
		}},
		{"content body, in a section rubric", contentBody, []string{"section rubric"}, []string{
			"/qti-content-body unexpected child element qti-feedback-block (checked as a child of qti-rubric-block of qti-assessment-section, where test.xml includes the fragment)",
		}},
		{"invalid content body", `<qti-content-body` + asi + `><qti-value>1</qti-value></qti-content-body>`, nil, []string{
			"/qti-content-body unexpected child element qti-value (checked as a child of qti-modal-feedback: the fragment " +
				"is valid in none of the 7 places the validator knows for a qti-content-body, and has the fewest errors in this one)",
		}},

		// A feedback block of a template block holds no interactions, and a
		// template block of a rubric block no feedback blocks.
		{"feedback block with an interaction, in an item body", feedback(asi, interaction), []string{"item body"}, nil},
		{"feedback block with an interaction, in a div", feedback(asi, interaction), []string{"div in item body"}, nil},
		{"feedback block with an interaction, in a template block", feedback(asi, interaction), []string{"template body"}, []string{
			"/qti-feedback-block/qti-content-body unexpected child element qti-extended-text-interaction (checked as a child of " +
				"qti-content-body of qti-template-block of qti-item-body, where items/item-1.xml includes the fragment)",
		}},
		{"template block with a feedback block, in an item body", template(feedback("", `<p>x</p>`)), []string{"item body"}, nil},
		{"template block with a feedback block, in an item rubric", template(feedback("", `<p>x</p>`)), []string{"item rubric body"}, []string{
			"/qti-template-block/qti-content-body unexpected child element qti-feedback-block (checked as a child of " +
				"qti-content-body of qti-rubric-block of qti-item-body, where items/item-1.xml includes the fragment)",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fragmentIn(t, tc.fragment, tc.places...)
			var got []string
			for _, e := range f.Errors {
				if e.Code != qti.CodeValidation || e.Line != 1 || e.File != "items/frag.xml" {
					t.Errorf("got %+v", e)
				}
				got = append(got, e.Path+" "+e.Message)
			}
			if !slices.Equal(got, tc.errors) || f.Valid != (len(tc.errors) == 0) || f.Skipped || f.Schema != qti.SchemaFragment || !f.Ran.Schema {
				t.Fatalf("got %+v\nerrors\n%q\nwant\n%q", f, got, tc.errors)
			}
		})
	}
}

// The findings of a fragment that is validated where it is included are
// located in the fragment as it is, whichever element that is.
func TestFragmentWhereIncludedFindingLocation(t *testing.T) {
	const fragment = "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n  <qti-rubric-block" + asi + ` view="candidate">` +
		"\n<qti-content-body><qti-value>1</qti-value></qti-content-body></qti-rubric-block>"
	f := fragmentIn(t, fragment, "section", "item body")
	if len(f.Errors) != 3 {
		t.Fatalf("got %+v", f.Errors)
	}
	// In the item body the root lacks its use attribute; the content is an
	// error there and in the section.
	for i, want := range []qti.Finding{
		{Line: 2, Column: 3, Path: "/qti-rubric-block"},
		{Line: 3, Column: 19, Path: "/qti-rubric-block/qti-content-body"},
		{Line: 3, Column: 19, Path: "/qti-rubric-block/qti-content-body"},
	} {
		if e := f.Errors[i]; e.Line != want.Line || e.Column != want.Column || e.Path != want.Path {
			t.Errorf("error %d: %+v", i, e)
		}
	}
}

// The errors of all the places where a fragment is not valid are within
// MAX_ERRORS together.
func TestFragmentWhereIncludedMaxErrors(t *testing.T) {
	const fragment = `<qti-rubric-block` + asi + ` view="candidate"><qti-content-body><qti-value>1</qti-value></qti-content-body></qti-rubric-block>`
	entries := withResources(t, "", includeFragment(t, "frag.xml"), entry{"items/frag.xml", fragment})
	entries[1].data = strings.Replace(entries[1].data, `visible="true">`,
		`visible="true"><xi:include xmlns:xi="http://www.w3.org/2001/XInclude" href="items/frag.xml"/>`, 1)
	data := makeZip(t, entries...)
	res := testValidator(t).WithLimits(qti.Limits{MaxErrors: 2}).ValidatePackage(context.Background(),
		app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
	if f := fileResult(t, res, "items/frag.xml"); f.Valid || len(f.Errors) != 2 {
		t.Fatalf("got %+v", f)
	}
}

// A document of a QTI resource that is not QTI 3, such as a QTI 2 item, is
// an error, also when another document includes it with XInclude: only a
// file that is merely listed as a file of a resource, or not listed, is
// skipped as XML that is not QTI content.
func TestPackageIncludedDocumentThatIsNotQTI3(t *testing.T) {
	const qti2 = `<assessmentItem xmlns="http://www.imsglobal.org/xsd/imsqti_v2p1" identifier="x"/>`
	const resource = `<resource identifier="old" type="imsqti_item_xmlv3p0" href="items/frag.xml"><file href="items/frag.xml"/></resource>`
	for name, tc := range map[string]struct {
		entries []entry
		skipped bool
	}{
		"the document of an item resource, included":      {withResources(t, resource, includeFragment(t, "frag.xml"), entry{"items/frag.xml", qti2}), false},
		"the document of an item resource, not included":  {withResources(t, resource, entry{"items/frag.xml", qti2}), false},
		"a file of the item that includes it":             {listAsItemFile(withResources(t, "", includeFragment(t, "frag.xml"), entry{"items/frag.xml", qti2})), true},
		"a file of the item, and the document of another": {listAsItemFile(withResources(t, resource, includeFragment(t, "frag.xml"), entry{"items/frag.xml", qti2})), false},
		"not listed, included":                            {withResources(t, "", includeFragment(t, "frag.xml"), entry{"items/frag.xml", qti2}), true},
	} {
		res := validatePackageWith(t, makeZip(t, tc.entries...), "")
		f := fileResult(t, res, "items/frag.xml")
		if tc.skipped {
			if !res.Valid || !f.Valid || !f.Skipped {
				t.Errorf("%s: got %+v", name, f)
			}
			continue
		}
		if res.Valid || f.Valid || f.Skipped || len(f.Errors) != 1 || f.Errors[0].Code != qti.CodeUnsupportedDocument ||
			!strings.Contains(f.Errors[0].Message, "{http://www.imsglobal.org/xsd/imsqti_v2p1}assessmentItem is not a supported QTI 3 document") {
			t.Errorf("%s: got %+v, package valid %v", name, f, res.Valid)
		}
		r := report.ForPackage(res, report.Meta{InputName: "p.zip", Now: time.Now()})
		if r.Summary.Fatals != 1 || len(r.Fatals) != 1 || r.Fatals[0].Location.Resource != "/items/frag.xml" {
			t.Errorf("%s: report %+v", name, r.Summary)
		}
	}
}
