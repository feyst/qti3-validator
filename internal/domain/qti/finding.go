package qti

// Code identifies the kind of a finding. Codes are part of the API; they are
// the validator's own and do not expose library codes.
type Code string

// The codes of findings.
const (
	CodeInvalidXML          Code = "invalid_xml"
	CodeUnsupportedEncoding Code = "unsupported_encoding"
	CodeUnsupportedXML      Code = "unsupported_xml"
	CodeUnsupportedDocument Code = "unsupported_document"
	CodeValidation          Code = "validation"
	CodeSchematron          Code = "schematron"
	CodeUnsupportedVersion  Code = "unsupported_version"
	CodeVersionOverridden   Code = "version_overridden"
	CodeLimitExceeded       Code = "limit_exceeded"
	CodeTooLarge            Code = "too_large"
	CodeInternal            Code = "internal"
	CodeInvalidZIP          Code = "invalid_zip"
	CodeUnsafePath          Code = "unsafe_path"
	CodeMissingManifest     Code = "missing_manifest"
	CodeTooManyFiles        Code = "too_many_files"
	CodeReference           Code = "reference"  // a reference that does not resolve in the package
	CodeValueType           Code = "value_type" // a value or expression of the wrong type
)

// Finding is one problem in a document or package.
type Finding struct {
	Code    Code
	File    string // entry in the package; empty for a single document
	Line    int
	Column  int
	Path    string // XPath of the element
	Rule    string // Schematron rule that fired: pattern id, and assertion id if any
	Source  string // file in the validators directory the finding came from
	Message string
}
