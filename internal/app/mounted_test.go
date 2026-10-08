package app_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"qti3-validator/internal/app"
	"qti3-validator/internal/bootstrap"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/testutil"
)

// Validators for a mounted directory. house-rules.sch applies to every
// document; my-metadata.xsd adds the root {urn:example:meta}meta, imports
// common.xsd next to it and the embedded LOM schema, and embeds a rule.
const (
	houseRules = `<?xml version="1.0" encoding="UTF-8"?>
<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron" queryBinding="xslt">
  <sch:ns prefix="qti" uri="http://www.imsglobal.org/xsd/imsqtiasi_v3p0"/>
  <sch:pattern id="HOUSE_TITLE">
    <sch:rule context="qti:qti-assessment-item">
      <sch:assert id="NO_DRAFT" test="not(contains(@title, 'DRAFT'))">Remove DRAFT from the title.</sch:assert>
    </sch:rule>
  </sch:pattern>
</sch:schema>`
	myMetadata = `<?xml version="1.0" encoding="UTF-8"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" xmlns:sch="http://purl.oclc.org/dsdl/schematron"
    xmlns:c="urn:example:common" xmlns:lom="http://ltsc.ieee.org/xsd/LOM"
    targetNamespace="urn:example:meta" elementFormDefault="qualified">
  <xs:import namespace="urn:example:common" schemaLocation="common.xsd"/>
  <xs:import namespace="http://ltsc.ieee.org/xsd/LOM"
      schemaLocation="https://purl.imsglobal.org/spec/md/v1p3/schema/xsd/imsmd_loose_v1p3p2.xsd"/>
  <xs:annotation><xs:appinfo>
    <sch:ns prefix="m" uri="urn:example:meta"/>
    <sch:pattern id="META_LEVEL">
      <sch:rule context="m:meta">
        <sch:assert test="m:level != 'none'">The level must not be none.</sch:assert>
      </sch:rule>
    </sch:pattern>
  </xs:appinfo></xs:annotation>
  <xs:element name="meta">
    <xs:complexType>
      <xs:sequence>
        <xs:element name="level" type="c:Level"/>
        <xs:element ref="lom:lom" minOccurs="0"/>
      </xs:sequence>
    </xs:complexType>
  </xs:element>
  <xs:element name="other-meta" type="xs:string"/>
</xs:schema>`
	commonXSD = `<?xml version="1.0" encoding="UTF-8"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:example:common">
  <xs:simpleType name="Level">
    <xs:restriction base="xs:string"><xs:maxLength value="5"/></xs:restriction>
  </xs:simpleType>
</xs:schema>`
)

// writeDir creates a validators directory with the given files.
func writeDir(t testing.TB, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

var (
	mountedOnce      sync.Once
	mountedValidator *app.Validator
	mountedErr       error
	mountedLoaded    map[string][]string
	mountedIgnored   map[string]string
)

// testMountedValidator compiles the embedded schemas plus a validators
// directory once for the whole package. The directory is read only at
// startup, so its removal after the first test does not matter.
func testMountedValidator(t testing.TB) *app.Validator {
	t.Helper()
	mountedOnce.Do(func() {
		dir := writeDir(t, map[string]string{
			"house-rules.sch":     houseRules,
			"my-metadata.xsd":     myMetadata,
			"common.xsd":          commonXSD,
			"README.txt":          "not a validator",
			"sub/ignored.sch":     "not even XML",
			"sub/also-ignored.md": "",
		})
		mountedLoaded, mountedIgnored = map[string][]string{}, map[string]string{}
		mountedValidator, mountedErr = bootstrap.NewValidator(bootstrap.Options{
			ValidatorsDir:      dir,
			OnValidatorLoaded:  func(file string, roots []string) { mountedLoaded[file] = roots },
			OnValidatorIgnored: func(file, reason string) { mountedIgnored[file] = reason },
		})
	})
	if mountedErr != nil {
		t.Fatal(mountedErr)
	}
	return mountedValidator
}

func TestMountedFilesLoadedAndIgnored(t *testing.T) {
	testMountedValidator(t)
	wantLoaded := map[string][]string{
		"common.xsd":      nil,
		"house-rules.sch": nil,
		"my-metadata.xsd": {"{urn:example:meta}meta", "{urn:example:meta}other-meta"},
	}
	if !reflect.DeepEqual(mountedLoaded, wantLoaded) {
		t.Errorf("loaded %v, want %v", mountedLoaded, wantLoaded)
	}
	wantIgnored := map[string]string{"README.txt": "not an .xsd or .sch file", "sub": "subdirectory"}
	if !reflect.DeepEqual(mountedIgnored, wantIgnored) {
		t.Errorf("ignored %v, want %v", mountedIgnored, wantIgnored)
	}
}

func TestMountedSchematronOnQTIItem(t *testing.T) {
	v := testMountedValidator(t)
	// A built-in rule (unknown attribute) and the mounted rule both fire.
	doc := strings.Replace(readTestdata(t, "invalid/schematron-unknown-attribute.xml"),
		`title="Hoofdstad"`, `title="Hoofdstad DRAFT"`, 1)
	for _, version := range qti.SupportedVersions() {
		res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(doc), Version: version})
		if res.Valid || res.Version != version {
			t.Fatalf("%s: got %+v", version, res)
		}
		var builtin, mounted int
		for _, e := range res.Errors {
			switch {
			case e.Rule == "HOUSE_TITLE/NO_DRAFT":
				mounted++
				if e.Source != "house-rules.sch" || e.Message != "Remove DRAFT from the title." || e.Line == 0 {
					t.Errorf("%s: mounted finding %+v", version, e)
				}
			case e.Source != "":
				t.Errorf("%s: built-in finding with source: %+v", version, e)
			default:
				builtin++
			}
		}
		if builtin == 0 || mounted != 1 {
			t.Fatalf("%s: want built-in findings and one mounted finding, got %+v", version, res.Errors)
		}
		// The mounted rules run after the built-in ones.
		if last := res.Errors[len(res.Errors)-1]; last.Source != "house-rules.sch" {
			t.Errorf("%s: last finding %+v, want the mounted one", version, last)
		}
	}
}

func TestMountedXSDAddsDocumentType(t *testing.T) {
	v := testMountedValidator(t)
	const lom = `<lom xmlns="http://ltsc.ieee.org/xsd/LOM"><general><title><string language="nl">Titel</string></title></general></lom>`
	tests := []struct {
		name    string
		doc     string
		schema  string
		valid   bool
		code    qti.Code
		message string
	}{
		{"valid", `<meta xmlns="urn:example:meta"><level>basic</level>` + lom + `</meta>`, "meta", true, "", ""},
		{"second root", `<other-meta xmlns="urn:example:meta">x</other-meta>`, "other-meta", true, "", ""},
		{
			"XSD error in an imported sibling type", `<meta xmlns="urn:example:meta"><level>much too long</level></meta>`,
			"meta", false, qti.CodeValidation, "",
		},
		{"missing element", `<meta xmlns="urn:example:meta"/>`, "meta", false, qti.CodeValidation, ""},
		{
			"error in the imported LOM", `<meta xmlns="urn:example:meta"><level>basic</level><lom xmlns="http://ltsc.ieee.org/xsd/LOM"><bogus/></lom></meta>`,
			"meta", false, qti.CodeValidation, "",
		},
		{
			"embedded Schematron", `<meta xmlns="urn:example:meta"><level>none</level></meta>`,
			"meta", false, qti.CodeSchematron, "The level must not be none.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(tt.doc), Version: "3.0.0"})
			if res.Valid != tt.valid || res.Version != "" || res.Schema != tt.schema {
				t.Fatalf("got %+v", res)
			}
			if tt.valid {
				if res.Outcome != qti.OutcomeValid || len(res.Errors) != 0 {
					t.Fatalf("got %+v", res)
				}
				return
			}
			if res.Outcome != qti.OutcomeInvalid || res.Errors[0].Code != tt.code || res.Errors[0].Source != "my-metadata.xsd" ||
				!strings.Contains(res.Errors[0].Message, tt.message) {
				t.Fatalf("got %+v", res.Errors)
			}
		})
	}
}

func TestMountedSchematronOnCustomDocument(t *testing.T) {
	dir := writeDir(t, map[string]string{
		"house.sch": `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <sch:ns prefix="m" uri="urn:example:meta"/>
  <sch:pattern id="LEVEL_SHORT">
    <sch:rule context="m:level"><sch:report test="string-length(.) &gt; 3" role="warning">Long level.</sch:report></sch:rule>
  </sch:pattern>
</sch:schema>`,
		"my-metadata.xsd": myMetadata,
		"common.xsd":      commonXSD,
	})
	v, err := bootstrap.NewValidator(bootstrap.Options{ValidatorsDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(`<meta xmlns="urn:example:meta"><level>basic</level></meta>`)})
	if !res.Valid || len(res.Warnings) != 1 || res.Warnings[0].Source != "house.sch" || res.Warnings[0].Message != "Long level." {
		t.Fatalf("got %+v", res)
	}
}

func TestMountedDocumentInPackage(t *testing.T) {
	v := testMountedValidator(t)
	entries := append(packageEntries(t),
		entry{"metadata/good.xml", `<meta xmlns="urn:example:meta"><level>basic</level></meta>`},
		entry{"metadata/bad.xml", `<meta xmlns="urn:example:meta"><level>none</level></meta>`})
	data := makeZip(t, entries...)
	res := v.ValidatePackage(context.Background(), app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
	if res.Valid || res.Outcome != qti.OutcomeInvalid {
		t.Fatalf("got %+v", res)
	}
	files := map[string]qti.FileResult{}
	for _, f := range res.Files {
		files[f.File] = f
	}
	if good := files["metadata/good.xml"]; !good.Valid || good.Skipped || good.Schema != "meta" || good.Version != "" {
		t.Errorf("good: %+v", good)
	}
	bad := files["metadata/bad.xml"]
	if bad.Valid || bad.Schema != "meta" || len(bad.Errors) != 1 ||
		bad.Errors[0].File != "metadata/bad.xml" || bad.Errors[0].Source != "my-metadata.xsd" {
		t.Errorf("bad: %+v", bad)
	}
	for _, name := range []string{"imsmanifest.xml", "test.xml", "items/item-1.xml"} {
		if f := files[name]; !f.Valid {
			t.Errorf("%s: %+v", name, f)
		}
	}
}

// Without a validators directory, and with one whose rules do not fire,
// the results for the fixtures are the same.
func TestMountedLeavesOtherResultsUnchanged(t *testing.T) {
	off, on := testValidator(t), testMountedValidator(t)
	files, err := filepath.Glob(testutil.Testdata("*/*.xml"))
	if err != nil || len(files) == 0 {
		t.Fatal("no fixtures", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, version := range []string{"", "3.0.0"} {
			a := off.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(data), Version: version})
			b := on.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(data), Version: version})
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s %q: without %+v, with %+v", f, version, a, b)
			}
			for _, e := range append(a.Errors, a.Warnings...) {
				if e.Source != "" {
					t.Errorf("%s: built-in finding with source: %+v", f, e)
				}
			}
		}
	}
	res := off.ValidateDocument(context.Background(), app.ValidateDocument{Document: strings.NewReader(`<meta xmlns="urn:example:meta"><level>x</level></meta>`)})
	if res.Errors[0].Code != qti.CodeUnsupportedDocument {
		t.Errorf("custom root without the directory: %+v", res)
	}
}

func TestMountedStartupErrors(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.xsd"), []byte(commonXSD), 0o644); err != nil {
		t.Fatal(err)
	}
	importing := func(location string) string {
		return `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:example:a">
  <xs:import namespace="urn:example:common" schemaLocation="` + location + `"/>
  <xs:element name="a" type="xs:string"/>
</xs:schema>`
	}
	declaring := func(namespace, root string) string {
		return `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="` + namespace + `">
  <xs:element name="` + root + `" type="xs:string"/>
</xs:schema>`
	}
	tests := []struct {
		name    string
		files   map[string]string
		symlink string // name of a link in the directory to outside.xsd
		want    []string
	}{
		{
			"import escaping the directory",
			map[string]string{"a.xsd": importing("../" + filepath.Base(outside) + "/outside.xsd")},
			"",
			[]string{"a.xsd", "not a file in the validators directory"},
		},
		{
			"import from a subdirectory",
			map[string]string{"a.xsd": importing("sub/common.xsd"), "sub/common.xsd": commonXSD},
			"",
			[]string{"a.xsd", "sub/common.xsd"},
		},
		{
			"absolute path import",
			map[string]string{"a.xsd": importing(filepath.Join(outside, "outside.xsd"))},
			"",
			[]string{"a.xsd", "not a file in the validators directory"},
		},
		{
			"network import",
			map[string]string{"a.xsd": importing("https://example.com/common.xsd")},
			"",
			[]string{"a.xsd", "https://example.com/common.xsd"},
		},
		{
			"symbolic link out of the directory",
			map[string]string{"a.xsd": importing("link.xsd")},
			"link.xsd",
			[]string{"link.xsd"},
		},
		{
			"collision with a built-in root",
			map[string]string{"qti.xsd": declaring(qti.NamespaceASI, "qti-assessment-item")},
			"",
			[]string{"qti.xsd", "qti-assessment-item", "built-in"},
		},
		{
			"collision between mounted XSDs",
			map[string]string{"a.xsd": declaring("urn:x", "r"), "b.xsd": declaring("urn:x", "r")},
			"",
			[]string{"b.xsd", "a.xsd", "{urn:x}r"},
		},
		{"XSD that does not compile", map[string]string{"broken.xsd": `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="r" type="xs:undefinedType"/></xs:schema>`}, "", []string{"broken.xsd", "undefinedType"}},
		{"not an XSD", map[string]string{"notes.xsd": `<notes/>`}, "", []string{"notes.xsd", "xs:schema"}},
		{"not well-formed", map[string]string{"bad.sch": `<sch:schema`}, "", []string{"bad.sch"}},
		{"not a Schematron schema", map[string]string{"x.sch": `<x/>`}, "", []string{"x.sch", "sch:schema"}},
		{
			"xslt2 query binding",
			map[string]string{"x2.sch": `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron" queryBinding="xslt2">
  <sch:pattern><sch:rule context="*"><sch:assert test="true()">x</sch:assert></sch:rule></sch:pattern></sch:schema>`},
			"",
			[]string{"x2.sch", "xslt2"},
		},
		{
			"unsupported XPath function",
			map[string]string{"key.sch": `<sch:schema xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <sch:pattern><sch:rule context="*"><sch:assert test="key('k', .)">x</sch:assert></sch:rule></sch:pattern></sch:schema>`},
			"",
			[]string{"key.sch", "key"},
		},
		{"embedded rule that does not compile", map[string]string{"emb.xsd": `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"
    xmlns:sch="http://purl.oclc.org/dsdl/schematron">
  <xs:annotation><xs:appinfo><sch:pattern><sch:rule context="*"><sch:assert test="document('x')">x</sch:assert></sch:rule></sch:pattern></xs:appinfo></xs:annotation>
  <xs:element name="r" type="xs:string"/></xs:schema>`}, "", []string{"emb.xsd", "document"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeDir(t, tt.files)
			if tt.symlink != "" {
				if err := os.Symlink(filepath.Join(outside, "outside.xsd"), filepath.Join(dir, tt.symlink)); err != nil {
					t.Fatal(err)
				}
			}
			_, err := bootstrap.NewValidator(bootstrap.Options{ValidatorsDir: dir})
			if err == nil {
				t.Fatal("want an error")
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}

func TestMountedDirectoryMissing(t *testing.T) {
	_, err := bootstrap.NewValidator(bootstrap.Options{ValidatorsDir: filepath.Join(t.TempDir(), "missing")})
	if err == nil || !strings.Contains(err.Error(), "validators directory") {
		t.Fatalf("got %v", err)
	}
}

// A symbolic link that stays inside the directory, as in a Kubernetes
// ConfigMap volume, is followed.
func TestMountedSymlinkInsideDirectory(t *testing.T) {
	dir := writeDir(t, map[string]string{"..data/house.sch": houseRules})
	if err := os.Symlink(filepath.Join("..data", "house.sch"), filepath.Join(dir, "house.sch")); err != nil {
		t.Fatal(err)
	}
	var loaded []string
	_, err := bootstrap.NewValidator(bootstrap.Options{ValidatorsDir: dir, OnValidatorLoaded: func(file string, _ []string) { loaded = append(loaded, file) }})
	if err != nil || !reflect.DeepEqual(loaded, []string{"house.sch"}) {
		t.Fatalf("loaded %v, err %v", loaded, err)
	}
}
