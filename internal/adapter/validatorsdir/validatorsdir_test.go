package validatorsdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qti3-validator/internal/adapter/schemastore"
	"qti3-validator/internal/adapter/xsdschema"
)

const smallXSD = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" targetNamespace="urn:x"><xs:element name="x"/></xs:schema>`

func TestFileTooLarge(t *testing.T) {
	defer func(old int64) { maxValidatorFileSize = old }(maxValidatorFileSize)
	maxValidatorFileSize = int64(len(smallXSD)) - 1
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.xsd"), []byte(smallXSD), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(Options{Dir: dir, Embedded: xsdschema.Resolver{Store: schemastore.Embedded()}})
	if err == nil || !strings.Contains(err.Error(), "small.xsd") || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadsTypesAndRules(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "small.xsd"), []byte(smallXSD), 0o600); err != nil {
		t.Fatal(err)
	}
	var loaded []string
	v, err := Load(Options{
		Dir: dir, Embedded: xsdschema.Resolver{Store: schemastore.Embedded()},
		Loaded: func(file string, roots []string) { loaded = append(loaded, file+" "+strings.Join(roots, ",")) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Types) != 1 || v.Types[0].Root != "x" || v.Types[0].File != "small.xsd" || v.Rules != nil {
		t.Fatalf("got %+v", v)
	}
	if len(loaded) != 1 || loaded[0] != "small.xsd {urn:x}x" {
		t.Fatalf("loaded %q", loaded)
	}
}
