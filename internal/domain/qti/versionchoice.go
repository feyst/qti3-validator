package qti

import (
	"fmt"
	"strings"
)

// SchemaVersionPath is where a manifest declares its QTI version, in the form
// the XML Schema validator reports paths.
const SchemaVersionPath = "/manifest/metadata/schemaversion"

// DeclaredVersion is the version a manifest declares, and where.
type DeclaredVersion struct {
	Value        string // empty when the document declares none
	Line, Column int
}

// VersionRequest is what selects the QTI version of a document.
type VersionRequest struct {
	// Forced is used whatever the document declares; it must be a supported
	// version. A manifest that declares another version is then not invalid
	// for that; a warning says the version was overridden.
	Forced string
	// Default is used when nothing is forced and the document declares no
	// version; a package passes its manifest's version. Empty or unsupported
	// selects the latest version.
	Default string
}

// VersionChoice is the version a document is validated against.
type VersionChoice struct {
	Version Version
	// Notice reports how a declared version was handled, or is nil. It is an
	// error unless NoticeIsWarning.
	Notice          *Finding
	NoticeIsWarning bool
	// ReplacesSchemaVerdict reports that the declared version is not the one
	// used, so the schema's verdict on it is replaced by Notice; see
	// DocumentResult.DropSchemaVersionVerdict.
	ReplacesSchemaVerdict bool
}

// ChooseVersion picks the QTI version for a document that declares
// declared, as req asks.
func ChooseVersion(req VersionRequest, declared DeclaredVersion) VersionChoice {
	at := func(f Finding) *Finding {
		f.Line, f.Column, f.Path = declared.Line, declared.Column, SchemaVersionPath
		return &f
	}
	if forced, ok := LookupVersion(req.Forced); ok {
		if declared.Value == "" || declared.Value == forced.Name {
			return VersionChoice{Version: forced}
		}
		return VersionChoice{
			Version: forced, NoticeIsWarning: true, ReplacesSchemaVerdict: true,
			Notice: at(Finding{
				Code: CodeVersionOverridden,
				Message: fmt.Sprintf("The manifest declares schemaversion %s; validated against QTI %s as requested.",
					declared.Value, forced.Name),
			}),
		}
	}
	if declared.Value != "" {
		if v, ok := LookupVersion(declared.Value); ok {
			return VersionChoice{Version: v}
		}
		latest := LatestVersion()
		return VersionChoice{
			Version: latest, ReplacesSchemaVerdict: true,
			Notice: at(Finding{
				Code: CodeUnsupportedVersion,
				Message: fmt.Sprintf("The manifest declares schemaversion %q, which is not supported (supported: %s); validated against QTI %s.",
					declared.Value, strings.Join(SupportedVersions(), ", "), latest.Name),
			}),
		}
	}
	if v, ok := LookupVersion(req.Default); ok {
		return VersionChoice{Version: v}
	}
	return VersionChoice{Version: LatestVersion()}
}

// DropSchemaVersionVerdict removes the schema's verdict on the manifest's
// schemaversion, which a version notice replaces.
func (r *DocumentResult) DropSchemaVersionVerdict() {
	kept := r.Errors[:0:0]
	for _, e := range r.Errors {
		if e.Code == CodeValidation && e.Path == SchemaVersionPath {
			continue
		}
		kept = append(kept, e)
	}
	r.Errors = kept
	if len(kept) == 0 && r.Outcome == OutcomeInvalid {
		r.Valid, r.Outcome = true, OutcomeValid
	}
}

// ApplyNotice adds the choice's notice, if any, to r.
func (c VersionChoice) ApplyNotice(r *DocumentResult) {
	switch {
	case c.Notice == nil:
	case c.NoticeIsWarning:
		r.Warnings = append(r.Warnings, *c.Notice)
	default:
		r.Errors = append([]Finding{*c.Notice}, r.Errors...)
		r.Valid, r.Outcome = false, r.Outcome.Worse(OutcomeInvalid)
	}
}
