package qti

import (
	"encoding/xml"
	"testing"
)

func TestLookupDocumentTypeNeedsNamespace(t *testing.T) {
	if _, ok := LookupDocumentType(xml.Name{Local: "qti-assessment-item"}); ok {
		t.Fatal("accepted qti-assessment-item without the QTI namespace")
	}
	if dt, ok := LookupDocumentType(xml.Name{Space: NamespaceManifest, Local: "manifest"}); !ok || dt.Schema != "imscp-manifest" {
		t.Fatalf("got %+v, %v", dt, ok)
	}
}

func TestSafeEntryName(t *testing.T) {
	for name, want := range map[string]bool{
		"imsmanifest.xml": true, "items/a.xml": true, "a..b.xml": true,
		"": false, "../a.xml": false, "a/../../b": false, "/a.xml": false, `a\b.xml`: false, "c:/a.xml": false, "a\x00.xml": false,
	} {
		if got := SafeEntryName(name); got != want {
			t.Errorf("SafeEntryName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestOutcomeWorse(t *testing.T) {
	if OutcomeInvalid.Worse(OutcomeMalformed) != OutcomeMalformed || OutcomeInternal.Worse(OutcomeValid) != OutcomeInternal {
		t.Fatal("Worse does not order by severity")
	}
}

func TestChooseVersion(t *testing.T) {
	latest := LatestVersion().Name
	declared := func(v string) DeclaredVersion { return DeclaredVersion{Value: v, Line: 4, Column: 5} }
	tests := []struct {
		name     string
		req      VersionRequest
		declared DeclaredVersion
		want     string
		notice   Code
		warning  bool
	}{
		{"nothing", VersionRequest{}, DeclaredVersion{}, latest, "", false},
		{"default", VersionRequest{Default: "3.0.0"}, DeclaredVersion{}, "3.0.0", "", false},
		{"unknown default", VersionRequest{Default: "9"}, DeclaredVersion{}, latest, "", false},
		{"declared", VersionRequest{Default: "3.0.1"}, declared("3.0.0"), "3.0.0", "", false},
		{"declared unsupported", VersionRequest{}, declared("2.2"), latest, CodeUnsupportedVersion, false},
		{"forced, same as declared", VersionRequest{Forced: "3.0.0"}, declared("3.0.0"), "3.0.0", "", false},
		{"forced over declared", VersionRequest{Forced: "3.0.1"}, declared("3.0.0"), "3.0.1", CodeVersionOverridden, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ChooseVersion(tt.req, tt.declared)
			if c.Version.Name != tt.want {
				t.Fatalf("version %s, want %s", c.Version.Name, tt.want)
			}
			var code Code
			if c.Notice != nil {
				code = c.Notice.Code
				if c.Notice.Line != 4 || c.Notice.Path != SchemaVersionPath {
					t.Fatalf("notice location %+v", c.Notice)
				}
			}
			if code != tt.notice || c.NoticeIsWarning != tt.warning || c.ReplacesSchemaVerdict != (tt.notice != "") {
				t.Fatalf("got %+v", c)
			}
		})
	}
}

func TestDropSchemaVersionVerdict(t *testing.T) {
	r := DocumentResult{Outcome: OutcomeInvalid, Errors: []Finding{{Code: CodeValidation, Path: SchemaVersionPath}}}
	r.DropSchemaVersionVerdict()
	if !r.Valid || r.Outcome != OutcomeValid || len(r.Errors) != 0 {
		t.Fatalf("got %+v", r)
	}
}
