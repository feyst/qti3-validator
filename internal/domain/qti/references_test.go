package qti

import (
	"slices"
	"testing"
)

func TestValidValue(t *testing.T) {
	for _, tc := range []struct {
		base  BaseType
		value string
		valid bool
	}{
		{Identifier, "choice_A-1.x", true},
		{Identifier, "1", false},
		{Identifier, "a b", false},
		{Boolean, "true", true},
		{Boolean, "yes", false},
		{Integer, "-12", true},
		{Integer, "1.5", false},
		{Float, "1.5e3", true},
		{Float, "INF", true},
		{Float, "0x10", false},
		{Float, "1,5", false},
		{Point, "10 20", true},
		{Point, "10", false},
		{DirectedPair, "A B", true},
		{Pair, "A", false},
		{String, "anything", true},
		{Integer, "  ", true}, // empty is NULL
	} {
		if got := ValidValue(tc.base, tc.value); got != tc.valid {
			t.Errorf("ValidValue(%s, %q) = %v", tc.base, tc.value, got)
		}
	}
}

func ref(kind RefKind, href string) Reference {
	return Reference{Kind: kind, Href: href, Element: "e", Line: 3}
}

func checks(findings []ReferenceFinding) []string {
	var out []string
	for _, f := range findings {
		s := f.File + " " + f.Rule
		if f.Warning {
			s += " warning"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func TestCheckReferences(t *testing.T) {
	p := PackageContents{
		Files: []string{ManifestName, "test.xml", "items/a.xml", "items/img/x.png", "items/lti.xml", "stimuli/s.xml", "modules/m.js", "stray.txt"},
		Documents: map[string]PackageDocument{
			ManifestName: {Schema: "imscp-manifest", Refs: DocumentReferences{References: []Reference{
				ref(RefManifestFile, "test.xml"), ref(RefManifestFile, "items/a.xml"), ref(RefManifestFile, "items/img/x.png"),
				ref(RefManifestFile, "items/lti.xml"), ref(RefManifestFile, "stimuli/s.xml"), ref(RefManifestFile, "modules/m.js"),
				ref(RefManifestFile, "missing.xml"),
			}}},
			"test.xml": {Schema: "qti-assessment-test", Refs: DocumentReferences{
				References: []Reference{ref(RefItem, "items/a.xml"), ref(RefItem, "items/lti.xml"), ref(RefItem, "stimuli/s.xml"), ref(RefItem, "items/gone.xml")},
				ItemRefs:   []ItemRef{{Identifier: "A", Href: "items/a.xml"}},
				VariableRefs: []VariableRef{
					{Item: "A", Variable: "SCORE"}, {Item: "A", Variable: "duration"}, {Item: "A", Variable: "NOPE"}, {Item: "SECTION", Variable: "duration"},
				},
			}},
			"items/a.xml": {Schema: "qti-assessment-item", Refs: DocumentReferences{
				Declared: []string{"RESPONSE", "SCORE"},
				References: []Reference{
					ref(RefAsset, "img/x.png"), ref(RefAsset, "img/y.png"), ref(RefAsset, "https://example.org/z.png"),
					ref(RefAsset, "../../outside.png"), ref(RefStimulus, "../stimuli/s.xml"),
					{Kind: RefModule, Href: "../modules/mXX.js", Fallback: "../modules/m.js", Element: "e"},
					{Kind: RefModule, Href: "../modules/m"},
					{Kind: RefModule, Href: "../modules/p", Fallback: "../modules/q"},
					ref(RefTemplateLocation, "rp.xml"),
				},
			}},
			"items/lti.xml": {Refs: DocumentReferences{Root: "cartridge_basiclti_link"}},
			"stimuli/s.xml": {Schema: "qti-assessment-stimulus"},
		},
	}
	got := checks(CheckReferences(p))
	want := []string{
		"imsmanifest.xml manifest-file-exists",
		"items/a.xml asset-exists",                     // img/y.png
		"items/a.xml asset-exists",                     // outside the package
		"items/a.xml pci-module-exists",                // neither path
		"items/a.xml pci-module-exists warning",        // fallback used
		"items/a.xml template-location-exists warning", // rp.xml
		"stray.txt file-in-manifest warning",
		"test.xml item-ref-exists", // items/gone.xml
		"test.xml item-ref-exists", // a stimulus, not an item
		"test.xml test-item-variable-declared",
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// Item-ref identifiers are unique only within a test: two tests that both
// call their item I1 are each checked against their own item, including
// items of the sections a test includes.
func TestCheckVariablesPerTest(t *testing.T) {
	item := func(declared ...string) PackageDocument {
		return PackageDocument{Schema: "qti-assessment-item", Refs: DocumentReferences{Declared: declared}}
	}
	test := func(href string, vars ...VariableRef) PackageDocument {
		return PackageDocument{Schema: "qti-assessment-test", Refs: DocumentReferences{
			References:   []Reference{ref(RefItem, href)},
			ItemRefs:     []ItemRef{{Identifier: "I1", Href: href}},
			VariableRefs: vars,
		}}
	}
	p := PackageContents{
		Files: []string{"a/test.xml", "a/item.xml", "b/test.xml", "b/item.xml", "c/test.xml", "c/section.xml", "c/sub.xml", "c/item.xml"},
		Documents: map[string]PackageDocument{
			"a/test.xml": test("item.xml", VariableRef{Item: "I1", Variable: "SCORE_A"}, VariableRef{Item: "I1", Variable: "SCORE_B"}),
			"a/item.xml": item("SCORE_A"),
			"b/test.xml": test("item.xml", VariableRef{Item: "I1", Variable: "SCORE_B"}, VariableRef{Item: "I1", Variable: "SCORE_A"}),
			"b/item.xml": item("SCORE_B"),
			// c/test.xml has no item refs of its own: its item is in a
			// section it includes, which includes another section.
			"c/test.xml": {Schema: "qti-assessment-test", Refs: DocumentReferences{
				References:   []Reference{ref(RefSection, "section.xml")},
				VariableRefs: []VariableRef{{Item: "C1", Variable: "SCORE_C"}, {Item: "C1", Variable: "NOPE"}},
			}},
			"c/section.xml": {Schema: "qti-assessment-section", Refs: DocumentReferences{References: []Reference{ref(RefSection, "sub.xml")}}},
			"c/sub.xml": {Schema: "qti-assessment-section", Refs: DocumentReferences{
				References: []Reference{ref(RefItem, "item.xml")},
				ItemRefs:   []ItemRef{{Identifier: "C1", Href: "item.xml"}},
			}},
			"c/item.xml": item("SCORE_C"),
		},
	}
	var got []string
	for _, f := range CheckReferences(p) {
		if f.Rule == CheckTestItemVariable {
			got = append(got, f.File+": "+f.Message)
		}
	}
	slices.Sort(got)
	want := []string{
		"a/test.xml: qti-variable refers to I1.SCORE_B, but a/item.xml declares no variable SCORE_B.",
		"b/test.xml: qti-variable refers to I1.SCORE_A, but b/item.xml declares no variable SCORE_A.",
		"c/test.xml: qti-variable refers to C1.NOPE, but c/item.xml declares no variable NOPE.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}

// An item-ref identifier used in two documents of one test, the test and a
// section it includes or two such sections, is reported on the test once;
// the rules see one document at a time and cannot. One used twice in one
// document is left to the rules, and two tests may share an identifier.
func TestItemRefIdentifierUniqueAcrossDocuments(t *testing.T) {
	item := PackageDocument{Schema: "qti-assessment-item", Refs: DocumentReferences{Declared: []string{"SCORE"}}}
	section := func(sections []string, items ...ItemRef) PackageDocument {
		d := PackageDocument{Schema: "qti-assessment-section", Refs: DocumentReferences{ItemRefs: items}}
		for _, s := range sections {
			d.Refs.References = append(d.Refs.References, ref(RefSection, s))
		}
		for _, ir := range items {
			d.Refs.References = append(d.Refs.References, ref(RefItem, ir.Href))
		}
		return d
	}
	test := func(sections []string, items ...ItemRef) PackageDocument {
		d := section(sections, items...)
		d.Schema = "qti-assessment-test"
		return d
	}
	p := PackageContents{
		Files: []string{
			"a/test.xml", "a/s1.xml", "a/i1.xml", "a/i2.xml",
			"b/test.xml", "b/s1.xml", "b/s2.xml", "b/i1.xml",
			"c/test.xml", "c/i1.xml", "c/i2.xml",
			"d/test.xml", "d/i1.xml",
		},
		Documents: map[string]PackageDocument{
			// The test and its section use I1 for different items, and the
			// section uses I2 twice (left to the rules).
			"a/test.xml": func() PackageDocument {
				d := test([]string{"s1.xml"}, ItemRef{Identifier: "I1", Href: "i1.xml"})
				d.Refs.VariableRefs = []VariableRef{{Item: "I1", Variable: "NOPE"}}
				return d
			}(),
			"a/s1.xml": section(nil, ItemRef{Identifier: "I1", Href: "i2.xml"}, ItemRef{Identifier: "I2", Href: "i1.xml"}, ItemRef{Identifier: "I2", Href: "i2.xml"}),
			"a/i1.xml": item, "a/i2.xml": item,
			// Two sections use I1 for the same item, three times in all.
			"b/test.xml": test([]string{"s1.xml", "s2.xml"}),
			"b/s1.xml":   section(nil, ItemRef{Identifier: "I1", Href: "i1.xml"}),
			"b/s2.xml":   section(nil, ItemRef{Identifier: "I1", Href: "i1.xml"}, ItemRef{Identifier: "I1", Href: "i1.xml"}),
			"b/i1.xml":   item,
			// One document: left to the rules.
			"c/test.xml": test(nil, ItemRef{Identifier: "I1", Href: "i1.xml"}, ItemRef{Identifier: "I1", Href: "i2.xml"}),
			"c/i1.xml":   item, "c/i2.xml": item,
			// Another test with I1: not a duplicate.
			"d/test.xml": test(nil, ItemRef{Identifier: "I1", Href: "i1.xml"}),
			"d/i1.xml":   item,
		},
	}
	var got []string
	for _, f := range CheckReferences(p) {
		if f.Warning {
			t.Errorf("warning: %+v", f)
		}
		got = append(got, f.File+" "+f.Rule+": "+f.Message)
	}
	slices.Sort(got)
	want := []string{
		`a/test.xml item-ref-identifier-unique: qti-assessment-item-ref identifier "I1" is used in both a/test.xml (for a/i1.xml) and a/s1.xml (for a/i2.xml) of test a/test.xml; item-ref identifiers must be unique within a test.`,
		`b/test.xml item-ref-identifier-unique: qti-assessment-item-ref identifier "I1" is used in both b/s1.xml (for b/i1.xml) and b/s2.xml (for b/i1.xml) of test b/test.xml; item-ref identifiers must be unique within a test.`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%q\nwant\n%q", got, want)
	}
}
