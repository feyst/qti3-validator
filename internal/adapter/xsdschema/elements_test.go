package xsdschema

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xsdelements"
	"qti3-validator/internal/testutil"
)

// pinnedSums returns the checksums in schemas.lock by URL.
func pinnedSums(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open(testutil.Path("internal/adapter/schemastore/schemas/schemas.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sums := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if fields := strings.Fields(scanner.Text()); len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			sums[fields[1]] = strings.ToLower(fields[0])
		}
	}
	return sums
}

// The element names the service loads are those the schemas declare: the
// file cmd/fetchschemas made of them is the one of the pinned schemas, of
// every QTI version and of nothing else, and holds what reading the embedded
// schemas gives now. A file left from before a change of schemas.lock, of
// the QTI versions or of the code that reads the names fails here.
func TestCompiledElementsAreThoseOfTheSchemas(t *testing.T) {
	store := schemastore.Embedded()
	compiled, err := store.Elements()
	if err != nil {
		t.Fatal(err)
	}
	const stale = "; run `go run ./cmd/fetchschemas` again"
	sums := pinnedSums(t)
	var want []string
	for _, v := range qti.Versions() {
		want = append(want, v.ASI)
		schema, ok := compiled.Schemas[v.ASI]
		if !ok {
			t.Fatalf("QTI %s: no compiled elements of %s%s", v.Name, v.ASI, stale)
		}
		if sums[v.ASI] == "" || schema.SHA256 != sums[v.ASI] {
			t.Errorf("QTI %s: the elements were read from the schema with checksum %s, schemas.lock pins %q%s",
				v.Name, schema.SHA256, sums[v.ASI], stale)
		}
		resolver := Resolver{Store: store}
		declared, err := DeclaredElements(resolver, v.ASI)
		if err != nil {
			t.Fatal(err)
		}
		got, err := CompiledElements(compiled, v.ASI)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, declared) {
			t.Errorf("QTI %s: the compiled elements are not those of the schema%s\ncompiled: %+v\ndeclared: %+v",
				v.Name, stale, got, declared)
		}

		// What the fragments of a package rely on.
		if got.Namespace != qti.NamespaceASI || !got.Global["qti-assessment-item"] || !got.Global["div"] ||
			!got.Local["qti-response-condition"] || got.Global["qti-response-condition"] || got.Declares("qti-bogus-element") {
			t.Errorf("QTI %s: elements %+v", v.Name, got)
		}
		const manyTyped = "qti-content-body qti-feedback-block qti-file-href qti-major-increment " +
			"qti-minor-increment qti-rubric-block qti-template-block qti-template-inline"
		if names := strings.Join(slices.Sorted(maps.Keys(got.ManyTyped)), " "); names != manyTyped {
			t.Errorf("QTI %s: declared with more than one type: %s\nwant: %s", v.Name, names, manyTyped)
		}
	}
	if got := slices.Sorted(maps.Keys(compiled.Schemas)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
		t.Errorf("compiled elements of %v, want those of %v%s", got, want, stale)
	}
}

// Compiled elements that are not the schema's are found, whatever differs.
func TestVerifyElementsFindsStaleElements(t *testing.T) {
	const asi = "https://example.org/x/schema.xsd"
	const schema = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:x" elementFormDefault="qualified">
		<xs:element name="a"><xs:complexType><xs:sequence>
			<xs:element name="b" type="xs:string"/><xs:element name="c" type="xs:string"/>
		</xs:sequence></xs:complexType></xs:element>
		<xs:element name="d"><xs:complexType><xs:sequence>
			<xs:element name="b" type="xs:int"/><xs:element name="c" type="xs:int"/>
		</xs:sequence></xs:complexType></xs:element>
	</xs:schema>`
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, err := zw.Write([]byte(schema)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Store: schemastore.New(fstest.MapFS{"example.org/x/schema.xsd.gz": {Data: gz.Bytes()}})}
	read, err := xsdelements.Read(strings.NewReader(schema))
	if err != nil {
		t.Fatal(err)
	}
	compiled := &xsdelements.Compiled{Version: xsdelements.CompiledVersion, Schemas: map[string]xsdelements.Schema{asi: {Elements: read}}}
	if err := VerifyElements(resolver, compiled, asi); err != nil {
		t.Fatalf("the elements as they were read: %v", err)
	}
	if want := (xsdelements.Elements{Namespace: "urn:x", Global: []string{"a", "d"}, Local: []string{"b", "c"}, ManyTyped: []string{"b", "c"}}); !reflect.DeepEqual(read, want) {
		t.Fatalf("read %+v, want %+v", read, want)
	}
	for name, change := range map[string]func(e *xsdelements.Elements){
		"namespace":           func(e *xsdelements.Elements) { e.Namespace = "urn:other" },
		"global missing":      func(e *xsdelements.Elements) { e.Global = e.Global[1:] },
		"local added":         func(e *xsdelements.Elements) { e.Local = append(slices.Clone(e.Local), "qti-new") },
		"many-typed missing":  func(e *xsdelements.Elements) { e.ManyTyped = e.ManyTyped[:len(e.ManyTyped)-1] },
		"many-typed replaced": func(e *xsdelements.Elements) { e.ManyTyped = []string{"a", "b"} },
	} {
		t.Run(name, func(t *testing.T) {
			schema := compiled.Schemas[asi]
			change(&schema.Elements)
			changed := &xsdelements.Compiled{Version: compiled.Version, Schemas: map[string]xsdelements.Schema{asi: schema}}
			if err := VerifyElements(resolver, changed, asi); err == nil || !strings.Contains(err.Error(), "fetchschemas") {
				t.Errorf("got %v, want an error that says to run fetchschemas", err)
			}
		})
	}
	// A schema without compiled elements, with and without reading it.
	empty := &xsdelements.Compiled{Version: compiled.Version}
	if _, err := CompiledElements(empty, asi); err == nil || !strings.Contains(err.Error(), "fetchschemas") {
		t.Errorf("got %v, want an error that says to run fetchschemas", err)
	}
	if err := VerifyElements(resolver, empty, asi); err == nil {
		t.Error("verified elements that are not there")
	}
}

func TestDeclaredElementsOfNoSchema(t *testing.T) {
	resolver := Resolver{Store: schemastore.Embedded()}
	if _, err := DeclaredElements(resolver, "https://example.org/missing.xsd"); err == nil {
		t.Error("read the elements of a schema that is not embedded")
	}
}
