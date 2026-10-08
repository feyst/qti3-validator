package qtirefs

import (
	"testing"

	"qti3-validator/internal/domain/qti"
)

func TestReadReferences(t *testing.T) {
	item := `<qti-assessment-item xmlns="` + qti.NamespaceASI + `" xmlns:xi="http://www.w3.org/2001/XInclude" identifier="i">
  <qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier"/>
  <qti-outcome-declaration identifier="SCORE" cardinality="single" base-type="float"/>
  <qti-stylesheet href="style.css" type="text/css"/>
  <qti-assessment-stimulus-ref identifier="s" href="s.xml"/>
  <qti-item-body>
    <img src="a.png" alt=""/>
    <video src="v.mp4" poster="p.png"><track src="t.vtt"/></video>
    <xi:include href="inc.xml"/>
    <qti-portable-custom-interaction response-identifier="RESPONSE" custom-interaction-type-identifier="x" module="m">
      <qti-interaction-modules primary-configuration="conf.json">
        <qti-interaction-module id="m" primary-path="m1" fallback-path="m2.js"/>
      </qti-interaction-modules>
    </qti-portable-custom-interaction>
    <qti-catalog-info><qti-catalog id="c"><qti-card><qti-file-href mime-type="audio/mpeg"> c.mp3 </qti-file-href></qti-card></qti-catalog></qti-catalog-info>
  </qti-item-body>
  <qti-response-processing template-location="rp.xml"/>
</qti-assessment-item>`
	refs, err := Reader{}.ReadReferences([]byte(item))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range refs.References {
		got = append(got, r.Element+"="+r.Href+"|"+r.Fallback)
	}
	want := []string{
		"qti-stylesheet=style.css|", "qti-assessment-stimulus-ref=s.xml|", "img=a.png|", "video=v.mp4|", "video=p.png|",
		"track=t.vtt|", "include=inc.xml|", "qti-interaction-modules=conf.json|", "qti-interaction-module=m1|m2.js",
		"qti-file-href=c.mp3|", "qti-response-processing=rp.xml|",
	}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("reference %d: got %s, want %s", i, got[i], want[i])
		}
	}
	if refs.Root != "qti-assessment-item" || len(refs.Declared) != 2 || refs.References[2].Line != 7 {
		t.Fatalf("got %+v", refs)
	}
}

func TestReadManifestAndTest(t *testing.T) {
	manifest := `<manifest xmlns="` + qti.NamespaceManifest + `"><resources xml:base="pkg/">
  <resource identifier="r" type="imsqti_item_xmlv3p0" href="a.xml" xml:base="items/"><file href="a.xml"/></resource>
  <resource identifier="w" type="webcontent" href="https://example.org/x"/>
</resources></manifest>`
	refs, err := Reader{}.ReadReferences([]byte(manifest))
	if err != nil || len(refs.References) != 3 || refs.References[0].Href != "pkg/items/a.xml" ||
		refs.References[1].Href != "pkg/items/a.xml" || refs.References[2].Href != "https://example.org/x" {
		t.Fatalf("manifest: %+v, %v", refs.References, err)
	}

	test := `<qti-assessment-test xmlns="` + qti.NamespaceASI + `" identifier="t" title="t">
  <qti-test-part identifier="p" navigation-mode="linear" submission-mode="individual">
    <qti-assessment-section identifier="s" title="s" visible="true">
      <qti-assessment-item-ref identifier="A" href="a.xml"/>
      <qti-assessment-section-ref identifier="S2" href="s2.xml"/>
    </qti-assessment-section>
  </qti-test-part>
  <qti-outcome-processing><qti-set-outcome-value identifier="T"><qti-variable identifier="A.SCORE"/></qti-set-outcome-value></qti-outcome-processing>
</qti-assessment-test>`
	refs, err = Reader{}.ReadReferences([]byte(test))
	if err != nil || len(refs.ItemRefs) != 1 || refs.ItemRefs[0].Identifier != "A" || len(refs.References) != 2 ||
		len(refs.VariableRefs) != 1 || refs.VariableRefs[0].Item != "A" || refs.VariableRefs[0].Variable != "SCORE" || refs.VariableRefs[0].Line != 8 {
		t.Fatalf("test: %+v, %v", refs, err)
	}
}
