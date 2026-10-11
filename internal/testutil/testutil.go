// Package testutil holds helpers shared by tests.
package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// root is the repository root, found from this file's location so tests in
// any package can use it.
var root = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}()

// Path returns the path of a file relative to the repository root.
func Path(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// Testdata returns the path of a file in testdata/.
func Testdata(name string) string { return Path("testdata/" + name) }

// ReadTestdata returns the contents of a file in testdata/.
func ReadTestdata(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(Testdata(name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// SlowRules is a Schematron file for a validators directory whose rule takes
// n³ steps on an item with n choices: every choice compares itself with
// every pair of choices, through current(), which the XPath engine cannot
// evaluate once for all choices. With 300 choices it runs for seconds.
const SlowRules = `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <sch:ns prefix="qti" uri="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>
  <sch:pattern id="SLOW">
    <sch:rule context="qti:qti-simple-choice">
      <sch:assert test="count(//qti:qti-simple-choice[count(//qti:qti-simple-choice[@identifier = current()/@identifier]) > 1]) = 0">Identifiers repeat.</sch:assert>
    </sch:rule>
  </sch:pattern>
</sch:schema>`

// ItemWithChoices returns a valid QTI item with one choice interaction of n
// choices, whose response declaration is missing: the additional checks find
// that error before any other rules run.
func ItemWithChoices(n int) string {
	var b strings.Builder
	b.WriteString(`<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false">`)
	b.WriteString(`<qti-item-body><qti-choice-interaction response-identifier="RESPONSE">`)
	for i := range n {
		fmt.Fprintf(&b, `<qti-simple-choice identifier="c%d">x</qti-simple-choice>`, i)
	}
	b.WriteString(`</qti-choice-interaction></qti-item-body></qti-assessment-item>`)
	return b.String()
}

// ItemWithDeclarations returns a QTI item with k response declarations and k
// choice interactions with max-choices="2", every third of which refers to a
// declaration. A 1EdTech rule compares every declaration with every
// interaction, at every interaction: k³ steps unless the engine evaluates
// its absolute paths once.
func ItemWithDeclarations(k int) string {
	var b strings.Builder
	b.WriteString(`<qti-assessment-item xmlns="http://www.imsglobal.org/xsd/imsqtiasi_v3p0" identifier="i" title="t" adaptive="false" time-dependent="false">`)
	for i := range k {
		fmt.Fprintf(&b, `<qti-response-declaration identifier="R%d" cardinality="single" base-type="identifier"/>`, i)
	}
	b.WriteString(`<qti-item-body>`)
	for i := range k {
		ref := fmt.Sprintf("X%d", i)
		if i%3 == 0 {
			ref = fmt.Sprintf("R%d", i)
		}
		fmt.Fprintf(&b, `<qti-choice-interaction response-identifier="%s" max-choices="2"><qti-simple-choice identifier="a">A</qti-simple-choice></qti-choice-interaction>`, ref)
	}
	b.WriteString(`</qti-item-body></qti-assessment-item>`)
	return b.String()
}
