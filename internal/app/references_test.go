package app_test

import (
	"strings"
	"testing"

	"qti3-validator/internal/domain/qti"
)

// referenceItem is an item that declares the outcome variable outcome.
func referenceItem(identifier, outcome string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="` + identifier + `" title="t" time-dependent="false">
  <qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="identifier">
    <qti-correct-response><qti-value>A</qti-value></qti-correct-response>
  </qti-response-declaration>
  <qti-outcome-declaration identifier="` + outcome + `" cardinality="single" base-type="float"/>
  <qti-item-body>
    <qti-choice-interaction response-identifier="RESPONSE" max-choices="1">
      <qti-simple-choice identifier="A">A</qti-simple-choice>
      <qti-simple-choice identifier="B">B</qti-simple-choice>
    </qti-choice-interaction>
  </qti-item-body>
  <qti-response-processing>
    <qti-response-condition><qti-response-if>
      <qti-match><qti-variable identifier="RESPONSE"/><qti-correct identifier="RESPONSE"/></qti-match>
      <qti-set-outcome-value identifier="` + outcome + `"><qti-base-value base-type="float">1</qti-base-value></qti-set-outcome-value>
    </qti-response-if></qti-response-condition>
  </qti-response-processing>
</qti-assessment-item>`
}

// referenceTest is a test with one item ref, I1 for item.xml, whose outcome
// processing reads I1.variable.
func referenceTest(identifier, variable string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<qti-assessment-test xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="` + identifier + `" title="T">
  <qti-outcome-declaration identifier="TOTAL" cardinality="single" base-type="float"/>
  <qti-test-part identifier="P" navigation-mode="linear" submission-mode="individual">
    <qti-assessment-section identifier="S" title="S" visible="true">
      <qti-assessment-item-ref identifier="I1" href="item.xml"/>
    </qti-assessment-section>
  </qti-test-part>
  <qti-outcome-processing>
    <qti-set-outcome-value identifier="TOTAL"><qti-variable identifier="I1.` + variable + `"/></qti-set-outcome-value>
  </qti-outcome-processing>
</qti-assessment-test>`
}

// referenceManifest lists resources, each a type and a file.
func referenceManifest(resources ...[2]string) string {
	var b strings.Builder
	for i, r := range resources {
		b.WriteString(`<resource identifier="r` + string(rune('a'+i)) + `" type="` + r[0] + `" href="` + r[1] + `"><file href="` + r[1] + `"/></resource>`)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<manifest xmlns="` + qti.NamespaceManifest + `" identifier="m"><metadata><schema>QTI Package</schema><schemaversion>3.0.0</schemaversion></metadata>
<organizations/><resources>` + b.String() + `</resources></manifest>`
}

// Two tests that both call their item I1 are each checked against their own
// item: neither gets the other's.
func TestTestsReusingAnItemRefIdentifier(t *testing.T) {
	const test, item = "imsqti_test_xmlv3p0", "imsqti_item_xmlv3p0"
	manifest := referenceManifest([2]string{test, "a/test.xml"}, [2]string{test, "b/test.xml"},
		[2]string{item, "a/item.xml"}, [2]string{item, "b/item.xml"})

	res := validatePackage(t, makeZip(t,
		entry{"imsmanifest.xml", manifest},
		entry{"a/test.xml", referenceTest("TA", "SCORE_A")},
		entry{"a/item.xml", referenceItem("ia", "SCORE_A")},
		entry{"b/test.xml", referenceTest("TB", "SCORE_B")},
		entry{"b/item.xml", referenceItem("ib", "SCORE_B")},
	), qti.PackageLimits{})
	if !res.Valid {
		t.Fatalf("each test reads a variable its own item declares; got %+v", res)
	}

	// a/test.xml reads SCORE_B, which only the item of b/test.xml declares.
	res = validatePackage(t, makeZip(t,
		entry{"imsmanifest.xml", manifest},
		entry{"a/test.xml", referenceTest("TA", "SCORE_B")},
		entry{"a/item.xml", referenceItem("ia", "SCORE_A")},
		entry{"b/test.xml", referenceTest("TB", "SCORE_B")},
		entry{"b/item.xml", referenceItem("ib", "SCORE_B")},
	), qti.PackageLimits{})
	f := fileResult(t, res, "a/test.xml")
	if res.Valid || len(f.Errors) != 1 || f.Errors[0].Rule != qti.CheckTestItemVariable ||
		f.Errors[0].Message != "qti-variable refers to I1.SCORE_B, but a/item.xml declares no variable SCORE_B." {
		t.Fatalf("want an error for I1.SCORE_B in a/test.xml; got %+v", res)
	}
}
