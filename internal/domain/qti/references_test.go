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
