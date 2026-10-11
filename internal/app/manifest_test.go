package app

import (
	"maps"
	"testing"

	"qti3-validator/internal/domain/qti"
)

func TestManifestResources(t *testing.T) {
	m := `<manifest xmlns="` + qti.NamespaceManifest + `" xml:base="pkg/"><resources xml:base="content/">
  <resource identifier="i" type="imsqti_item_xmlv3p0" href="item.xml" xml:base="items/">
    <file href="item.xml"/><file href="shared.xml"/><file href="img%20a.png"/><file href="100%.png"/>
  </resource>
  <resource identifier="f" type="imsqti_fragment_xmlv3p0" href="items/shared.xml"><file href="items/shared.xml"/></resource>
  <resource identifier="o" type="imsqti_outcomes_xmlv3p0" href="https://example.org/o.xml"/>
  <resource identifier="w" type="webcontent" href="page.xml"><file href="page.xml"/></resource>
  <resource identifier="x" type="imsqti_item_xmlv3p0" href="../../../outside.xml"/>
</resources>
<other xmlns="urn:x"><resource type="imsqti_item_xmlv3p0" href="not-a-cp-element.xml"/></other>
</manifest>`
	got := manifestResources([]byte(m))
	want := map[string]qti.ResourceKind{
		"pkg/content/items/item.xml":   qti.ResourceDocument,
		"pkg/content/items/shared.xml": qti.ResourceFragment, // a fragment, though the item lists it too
		"pkg/content/items/img a.png":  qti.ResourceDocument,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	// A manifest in another namespace is not a QTI 3 manifest: its
	// resources say nothing.
	if got := manifestResources([]byte(`<manifest xmlns="http://www.imsglobal.org/xsd/imscp_v1p1"><resources>` +
		`<resource type="imsqti_item_xmlv3p0" href="item.xml"/></resources></manifest>`)); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
