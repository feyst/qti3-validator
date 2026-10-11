package qti

import (
	"slices"
	"strings"
	"testing"
)

func TestClassifyResourceType(t *testing.T) {
	for typ, want := range map[string]ResourceKind{
		"imsqti_item_xmlv3p0":     ResourceDocument,
		"imsqti_outcomes_xmlv3p0": ResourceDocument,
		"imsqti_fragment_xmlv3p0": ResourceFragment,
		"webcontent":              ResourceOther,
		"":                        ResourceOther,
	} {
		if got := ClassifyResourceType(typ); got != want {
			t.Errorf("%q: got %v, want %v", typ, got, want)
		}
	}
}

func TestBaseStack(t *testing.T) {
	var s BaseStack
	steps := []struct{ base, want string }{
		{"", ""},                // manifest
		{"pkg/", "pkg/"},        // resources
		{"items/", "pkg/items"}, // resource
		{"", "pkg/items"},       // file, inherits
		{"https://example.org/", "https://example.org/"},
	}
	for _, st := range steps {
		if got := s.Push(st.base); got != st.want {
			t.Errorf("Push(%q) = %q, want %q", st.base, got, st.want)
		}
	}
	s.Pop()
	s.Pop()
	if got := s.Push(""); got != "pkg/items" {
		t.Errorf("after Pop: %q", got)
	}
	for _, tc := range []struct{ base, href, want string }{
		{"", "a.xml", "a.xml"},
		{"items/", "a.xml", "items/a.xml"},
		{"items/", "https://example.org/a.xml", "https://example.org/a.xml"},
		{"https://example.org/x/", "a.xml", "https://example.org/x/a.xml"},
	} {
		if got := JoinBase(tc.base, tc.href); got != tc.want {
			t.Errorf("JoinBase(%q, %q) = %q, want %q", tc.base, tc.href, got, tc.want)
		}
	}
}

func TestPackagePath(t *testing.T) {
	for _, tc := range []struct {
		doc, href, want string
		ok              bool
	}{
		{ManifestName, "items/a%20b.xml", "items/a b.xml", true},
		{"items/i.xml", "../media/x.png", "media/x.png", true},
		{ManifestName, "../x.xml", "", false},
		{ManifestName, "https://example.org/x.xml", "", false},
		{ManifestName, "100%.png", "", false}, // not a valid URI reference
	} {
		got, ok := PackagePath(tc.doc, tc.href)
		if ok != tc.ok || ok && got != tc.want {
			t.Errorf("PackagePath(%q, %q) = %q, %v", tc.doc, tc.href, got, ok)
		}
	}
}

// An href that is not a valid URI reference is reported as such, not as
// outside the package, and the file it names is not reported as unlisted
// as well.
func TestCheckReferencesInvalidHref(t *testing.T) {
	p := PackageContents{
		Files: []string{ManifestName, "item.xml", "100%.png"},
		Documents: map[string]PackageDocument{
			ManifestName: {Schema: SchemaManifest, Refs: DocumentReferences{References: []Reference{
				// A resource and its file name it: one finding.
				ref(RefManifestFile, "item.xml"), ref(RefManifestFile, "100%.png"), ref(RefManifestFile, "100%.png"),
			}}},
			"item.xml": {Schema: "qti-assessment-item", Refs: DocumentReferences{References: []Reference{ref(RefAsset, "100%.png")}}},
		},
	}
	findings := CheckReferences(p)
	if got, want := checks(findings), []string{"imsmanifest.xml manifest-file-exists", "item.xml asset-exists"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, f := range findings {
		if !strings.Contains(f.Message, "not a valid URI reference") || strings.Contains(f.Message, "outside") {
			t.Errorf("message: %s", f.Message)
		}
	}
}

// A reference to a file that could not be read gets no finding about its
// document type: the file's own error says what is wrong.
func TestCheckReferencesToUnreadableFile(t *testing.T) {
	p := PackageContents{
		Files: []string{"test.xml", "item.xml"},
		Documents: map[string]PackageDocument{
			"test.xml": {Schema: "qti-assessment-test", Refs: DocumentReferences{
				References:   []Reference{ref(RefItem, "item.xml")},
				ItemRefs:     []ItemRef{{Identifier: "I", Href: "item.xml"}},
				VariableRefs: []VariableRef{{Item: "I", Variable: "SCORE"}},
			}},
		},
		Unreadable: map[string]bool{"item.xml": true},
	}
	if got := CheckReferences(p); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	// Without Unreadable, item.xml is a file that is not a QTI document.
	p.Unreadable = nil
	if got := checks(CheckReferences(p)); !slices.Equal(got, []string{"test.xml item-ref-exists"}) {
		t.Fatalf("got %q", got)
	}
}

// Without a QTI 3 manifest, files are not reported as unlisted one by one;
// the manifest's own validation reports it.
func TestCheckReferencesWithoutQTIManifest(t *testing.T) {
	p := PackageContents{
		Files:     []string{ManifestName, "item.xml", "a.png"},
		Documents: map[string]PackageDocument{ManifestName: {}, "item.xml": {Schema: "qti-assessment-item"}},
	}
	if got := CheckReferences(p); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
}
