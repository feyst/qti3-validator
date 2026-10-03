package qti

// DocumentResult is the result of validating one XML document.
type DocumentResult struct {
	Valid    bool
	Schema   string // the document type's schema name; empty when unknown
	Version  string // QTI version validated against; empty for custom types
	Errors   []Finding
	Warnings []Finding // findings that leave the document valid
	Outcome  Outcome
}

// Failure is the result of a document that could not be validated at all.
func Failure(outcome Outcome, schema string, code Code, message string) DocumentResult {
	return DocumentResult{
		Schema:  schema,
		Errors:  []Finding{{Code: code, Message: message}},
		Outcome: outcome,
	}
}

// AddError records an error with the outcome it implies.
func (r *DocumentResult) AddError(f Finding, outcome Outcome) {
	r.Errors = append(r.Errors, f)
	r.Valid = false
	r.Outcome = r.Outcome.Worse(outcome)
}

// PackageResult is the result of validating a QTI package.
type PackageResult struct {
	Valid    bool
	Version  string    // QTI version the package was validated against
	Errors   []Finding // findings about the package itself
	Warnings []Finding // findings about the package that leave it valid
	Files    []FileResult
	Outcome  Outcome
}

// Fail records a finding about the package itself.
func (r *PackageResult) Fail(outcome Outcome, f Finding) {
	r.Errors = append(r.Errors, f)
	r.Outcome = r.Outcome.Worse(outcome)
}

// FileResult is the result for one XML file in a package. A skipped file is
// XML that is not a QTI document, such as a stray .xml resource; it does not
// make the package invalid.
type FileResult struct {
	File     string
	Valid    bool
	Skipped  bool
	Schema   string
	Version  string
	Errors   []Finding
	Warnings []Finding
}
