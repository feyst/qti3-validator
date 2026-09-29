package app_test

import (
	"strings"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

func TestItemVersion(t *testing.T) {
	// audio/@autoplay="autoplay" is allowed from QTI 3.0.1 on.
	doc := readTestdata(t, "valid/assessment-item-3.0.1.xml")
	tests := []struct {
		opts    string // forced version
		version string
		valid   bool
	}{
		{"", "3.0.1", true},
		{"3.0.1", "3.0.1", true},
		{"3.0.0", "3.0.0", false},
	}
	for _, tt := range tests {
		res := validateString(t, doc, tt.opts)
		if res.Version != tt.version || res.Valid != tt.valid {
			t.Errorf("%+v: got version %q valid %v, want %q %v: %+v", tt.opts, res.Version, res.Valid, tt.version, tt.valid, res.Errors)
		}
	}
}

func TestManifestDeclaresItsVersion(t *testing.T) {
	manifest := readTestdata(t, "package/imsmanifest.xml") // schemaversion 3.0.0
	for _, declared := range []string{"3.0.0", "3.0.1"} {
		doc := strings.Replace(manifest, "<schemaversion>3.0.0</schemaversion>", "<schemaversion>"+declared+"</schemaversion>", 1)
		res := validateString(t, doc, "")
		if !res.Valid || res.Version != declared || len(res.Warnings) != 0 {
			t.Errorf("schemaversion %s: got %+v", declared, res)
		}
	}
}

func TestVersionOverrideReplacesSchemaVersionError(t *testing.T) {
	manifest := readTestdata(t, "package/imsmanifest.xml")
	res := validateString(t, manifest, "3.0.1")
	if !res.Valid || res.Version != "3.0.1" || len(res.Errors) != 0 {
		t.Fatalf("got %+v", res)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != qti.CodeVersionOverridden || res.Warnings[0].Path != qti.SchemaVersionPath ||
		res.Warnings[0].Line != 5 || !strings.Contains(res.Warnings[0].Message, "3.0.0") {
		t.Fatalf("warnings: %+v", res.Warnings)
	}
}

// Only the schemaversion error is dropped; other problems stay.
func TestVersionOverrideKeepsOtherErrors(t *testing.T) {
	manifest := strings.Replace(readTestdata(t, "package/imsmanifest.xml"),
		`type="imsqti_item_xmlv3p0"`, `type="not-a-resource-type"`, 1)
	res := validateString(t, manifest, "3.0.1")
	if res.Valid || len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeValidation || res.Errors[0].Path == qti.SchemaVersionPath {
		t.Fatalf("got %+v", res.Errors)
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Code != qti.CodeVersionOverridden {
		t.Fatalf("warnings: %+v", res.Warnings)
	}
}

func TestUnsupportedManifestVersion(t *testing.T) {
	manifest := strings.Replace(readTestdata(t, "package/imsmanifest.xml"),
		"<schemaversion>3.0.0</schemaversion>", "<schemaversion>3.0.2</schemaversion>", 1)
	res := validateString(t, manifest, "")
	if res.Valid || res.Outcome != qti.OutcomeInvalid || res.Version != qti.LatestVersion().Name {
		t.Fatalf("got %+v", res)
	}
	if len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeUnsupportedVersion || !strings.Contains(res.Errors[0].Message, "3.0.2") {
		t.Fatalf("want only unsupported_version, got %+v", res.Errors)
	}
}

func TestPackageUsesManifestVersion(t *testing.T) {
	entries := packageEntries(t) // manifest declares 3.0.0
	res := validatePackageWith(t, makeZip(t, entries...), "")
	if !res.Valid || res.Version != "3.0.0" {
		t.Fatalf("got %+v", res)
	}
	for _, f := range res.Files {
		if f.Version != "3.0.0" {
			t.Errorf("%s validated against %s", f.File, f.Version)
		}
	}

	// A 3.0.1-only item in a 3.0.0 package is invalid, unless 3.0.1 is forced.
	entries = append(entries, entry{"items/item-301.xml", readTestdata(t, "valid/assessment-item-3.0.1.xml")})
	if res := validatePackageWith(t, makeZip(t, entries...), ""); res.Valid {
		t.Fatalf("3.0.1 item accepted in a 3.0.0 package: %+v", res)
	}
	res = validatePackageWith(t, makeZip(t, entries...), "3.0.1")
	if !res.Valid || res.Version != "3.0.1" {
		t.Fatalf("forced 3.0.1: %+v", res)
	}
	if m := fileResult(t, res, "imsmanifest.xml"); len(m.Warnings) != 1 || m.Warnings[0].Code != qti.CodeVersionOverridden {
		t.Fatalf("manifest: %+v", m)
	}
}

func TestLatestVersion(t *testing.T) {
	if qti.LatestVersion().Name != "3.0.1" {
		t.Errorf("latest = %s", qti.LatestVersion().Name)
	}
}
