// Package report is the read model of a validation: the report clients
// receive, projected from the domain results of internal/domain/qti.
package report

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/kennisnet/qti3-validator/internal/domain/qti"
)

// Report is the validation report both endpoints return, for a single XML
// document and for a package alike.
//
// Its shape follows the public report model of 1EdTech's validator engine
// ("inspector", which calls itself "IMS Global Member Validator 3"), so that
// people who know 1EdTech's validators recognise it. The model is derived
// only from these public sources:
//
//   - The OpenAPI description of the public 1EdTech validator, schemas
//     Report, Summary, ReportItem and Location and the Outcome enum:
//     https://vc.1ed.tech/v3/api-docs
//   - The open-source validator built on that engine, its README and test
//     fixtures: https://github.com/1EdTech/digital-credentials-public-validator
//   - The public engine libraries, whose Location types include a text
//     location with line and column for XML, and whose ZipResource addresses
//     a file inside a package from the package root:
//     https://nexus.1edtech.net/repository/1edtech-public-release/org/1edtech/inspector-core/1.9.1/
//     https://nexus.1edtech.net/repository/1edtech-public-release/org/1edtech/inspector-util/1.9.1/
//
// Our own additions, which that model does not have, are marked as such on
// the fields: Item.Code, Item.Source and Location.Path, and one VALID item
// per document that passed.
type Report struct {
	// ID is a random UUID, as in the public validator's reports.
	ID string `json:"id"`
	// Generated is when the report was made, in UTC, written as the public
	// validator's reports write it: 2026-10-07T07:49:53.
	Generated     string        `json:"generated"`
	Generator     string        `json:"generator"` // the service and its version
	Input         Input         `json:"input"`
	Specification Specification `json:"specification"`
	Summary       Summary       `json:"summary"`
	// One list per outcome, always present, as in the public model.
	Fatals     []Item `json:"fatals"`
	Errors     []Item `json:"errors"`
	Warnings   []Item `json:"warnings"`
	Exceptions []Item `json:"exceptions"`
	NotRun     []Item `json:"notRun"`
	Valids     []Item `json:"valids"`
}

// Input names what was validated: the public model's {name, type}.
type Input struct {
	Name string `json:"name"`
	Type string `json:"type"` // "XML" or "ZIP"
}

// Specification names what the input was validated against: the public
// model's {pid, shortName, version, title}. The pid follows the form of the
// public validator's "ob30.pid": short name and version digits.
type Specification struct {
	PID       string `json:"pid"`
	ShortName string `json:"shortName"`
	Version   string `json:"version,omitempty"`
	Title     string `json:"title"`
}

// Summary is the public model's summary: the worst outcome and a count per
// outcome. In the public validator's reports TotalRun counts the checks that
// ran (14 for one credential), so here it counts checks as well: reading
// each document, its XML Schema validation and its Schematron rules, and the
// package itself. Valid counts the VALID items.
type Summary struct {
	Outcome    string `json:"outcome"`
	Fatals     int    `json:"fatals"`
	Errors     int    `json:"errors"`
	Warnings   int    `json:"warnings"`
	Exceptions int    `json:"exceptions"`
	NotRun     int    `json:"notRun"`
	TotalRun   int    `json:"totalRun"`
	Valid      int    `json:"valid"`
}

// Item is one finding: the public model's ReportItem with title, message,
// location, generator and detailsMessage.
type Item struct {
	Title    string   `json:"title"`
	Message  string   `json:"message"`
	Location Location `json:"location"`
	// Generator identifies the check that produced the item. In the public
	// model it is the probe's name, optionally followed by "|" and the
	// schema it used ("JsonSchemaProbe|https://…"); here, for example,
	// "xsd|https://purl.imsglobal.org/…", "schematron|RULE_SET_…" or, for
	// the validator's own rules, "schematron|qti3-additional-checks.sch#…".
	Generator string `json:"generator"`
	// Code is a stable machine-readable code. Our addition.
	Code string `json:"code"`
	// Source names the file in the validators directory the finding came
	// from; empty for built-in checks. Our addition.
	Source         string  `json:"source,omitempty"`
	DetailsMessage *string `json:"detailsMessage"`

	// Outcome is the list the item belongs to; the lists themselves carry it
	// in the JSON, as in the public model.
	Outcome string `json:"-"`
}

// Location is the public model's location: the resource, plus line and
// column for a text location. The public API description does not define
// the location's fields. In the public engine library (inspector-core
// 1.9.1, TextLocation) they have the getters getLine and getColumn, and the
// report is serialised from getters: the public validator at vc.1ed.tech
// shows ReportItem.getDetailsMessage as "detailsMessage", although the
// field is detailMessage. So the names are "line" and "column".
type Location struct {
	Resource string `json:"resource"`
	Line     int    `json:"line,omitempty"`
	Column   int    `json:"column,omitempty"`
	// Path is the XPath of the element. The public model's path is a
	// JSONPath for JSON input; for XML we give an XPath. Our addition.
	Path string `json:"path,omitempty"`
}

// The outcomes of the public model, in increasing severity: the Outcome enum
// NOT_RUN, VALID, WARNING, ERROR, FATAL, EXCEPTION. A report's outcome is the
// most severe one among its items.
const (
	OutcomeNameNotRun    = "NOT_RUN"
	OutcomeNameValid     = "VALID"
	OutcomeNameWarning   = "WARNING"
	OutcomeNameError     = "ERROR"
	OutcomeNameFatal     = "FATAL"
	OutcomeNameException = "EXCEPTION"
)

var outcomeRank = map[string]int{
	OutcomeNameNotRun: 0, OutcomeNameValid: 1, OutcomeNameWarning: 2,
	OutcomeNameError: 3, OutcomeNameFatal: 4, OutcomeNameException: 5,
}

// IsValid reports whether the report passed: nothing worse than a warning.
// Older IMS validators used the same rule: "If you experience only warnings
// and no errors, the cartridge is still considered valid."
func (r Report) IsValid() bool {
	return outcomeRank[r.Summary.Outcome] <= outcomeRank[OutcomeNameWarning]
}

// Meta is what a report needs besides the validation result.
type Meta struct {
	Generator string
	InputName string
	Now       time.Time
}

// ForDocument turns the result of one XML document into a report.
func ForDocument(res qti.DocumentResult, meta Meta) Report {
	b := newReportBuilder(meta, "XML", res.Version)
	b.addDocument(meta.InputName, res, false)
	return b.report
}

// ForPackage turns the result of a package into a report.
func ForPackage(res qti.PackageResult, meta Meta) Report {
	b := newReportBuilder(meta, "ZIP", res.Version)
	b.report.Summary.TotalRun++ // reading the package and its manifest
	for _, e := range res.Errors {
		resource := meta.InputName
		if e.File != "" {
			resource = packagePath(e.File)
		}
		b.add(itemFor(e, resource, "", ""))
	}
	for _, f := range res.Files {
		b.addDocument(packagePath(f.File), qti.DocumentResult{
			Valid: f.Valid, Schema: f.Schema, Version: f.Version,
			Errors: f.Errors, Warnings: f.Warnings,
		}, f.Skipped)
	}
	return b.report
}

// packagePath names a file inside a package from the package root, with a
// leading slash. The public inspector-util library's ZipResource addresses
// package entries the same way: as a jar URI whose entry path follows "!/".
func packagePath(name string) string {
	return "/" + strings.TrimPrefix(name, "/")
}

type reportBuilder struct{ report Report }

func newReportBuilder(meta Meta, inputType, version string) *reportBuilder {
	title := "QTI"
	if version != "" {
		title += " " + version
	}
	return &reportBuilder{report: Report{
		ID:            newReportID(),
		Generated:     meta.Now.UTC().Format("2006-01-02T15:04:05"),
		Generator:     meta.Generator,
		Input:         Input{Name: meta.InputName, Type: inputType},
		Specification: Specification{PID: "qti" + strings.ReplaceAll(version, ".", "") + ".pid", ShortName: "qti", Version: version, Title: title},
		Summary:       Summary{Outcome: OutcomeNameValid},
		Fatals:        []Item{}, Errors: []Item{}, Warnings: []Item{},
		Exceptions: []Item{}, NotRun: []Item{}, Valids: []Item{},
	}}
}

// addDocument adds the findings of one document, and a VALID item when it
// passed or a NOT_RUN item when it was skipped or could not be read.
func (b *reportBuilder) addDocument(resource string, res qti.DocumentResult, skipped bool) {
	b.report.Summary.TotalRun++ // reading the document
	if skipped {
		b.add(Item{
			Title: "Document type", Message: "Not a QTI document; not validated.",
			Location: Location{Resource: resource}, Generator: "document-type", Code: "skipped", Outcome: OutcomeNameNotRun,
		})
		return
	}
	fatal := false
	for _, e := range res.Errors {
		it := itemFor(e, resource, res.Schema, res.Version)
		fatal = fatal || it.Outcome == OutcomeNameFatal
		b.add(it)
	}
	for _, e := range res.Warnings {
		it := itemFor(e, resource, res.Schema, res.Version)
		it.Outcome = OutcomeNameWarning
		b.add(it)
	}
	if !fatal {
		b.report.Summary.TotalRun += 2 // XML Schema and Schematron
	}
	switch {
	case fatal:
		b.add(Item{
			Title: "XML Schema and Schematron", Message: "Not run: the document could not be read.",
			Location: Location{Resource: resource}, Generator: "validation", Code: "not_run", Outcome: OutcomeNameNotRun,
		})
	case res.Valid:
		b.report.Summary.Valid++
		b.add(Item{
			Title: "Document", Message: "Valid " + documentLabel(res), Location: Location{Resource: resource},
			Generator: "validation", Code: "valid", Outcome: OutcomeNameValid,
		})
	}
}

func documentLabel(res qti.DocumentResult) string {
	label := res.Schema
	if res.Version != "" {
		label += " (QTI " + res.Version + ")"
	}
	return label
}

// add files an item under its outcome and updates the summary.
func (b *reportBuilder) add(it Item) {
	r := &b.report
	switch it.Outcome {
	case OutcomeNameFatal:
		r.Fatals = append(r.Fatals, it)
		r.Summary.Fatals++
	case OutcomeNameError:
		r.Errors = append(r.Errors, it)
		r.Summary.Errors++
	case OutcomeNameWarning:
		r.Warnings = append(r.Warnings, it)
		r.Summary.Warnings++
	case OutcomeNameException:
		r.Exceptions = append(r.Exceptions, it)
		r.Summary.Exceptions++
	case OutcomeNameNotRun:
		r.NotRun = append(r.NotRun, it)
		r.Summary.NotRun++
	default:
		r.Valids = append(r.Valids, it)
		return // a VALID item never raises the outcome
	}
	if it.Outcome != OutcomeNameNotRun && outcomeRank[it.Outcome] > outcomeRank[r.Summary.Outcome] {
		r.Summary.Outcome = it.Outcome
	}
}

// itemFor maps a finding to a report item: its outcome, title and generator.
func itemFor(e qti.Finding, resource, schema, version string) Item {
	it := Item{
		Message:  e.Message,
		Location: Location{Resource: resource, Line: e.Line, Column: e.Column, Path: e.Path},
		Code:     string(e.Code),
		Source:   e.Source,
		Outcome:  OutcomeNameError,
	}
	switch e.Code {
	case qti.CodeValidation:
		it.Title, it.Generator = "XML Schema validation", "xsd|"+schemaFor(schema, version, e.Source)
	case qti.CodeSchematron:
		it.Title, it.Generator = "Schematron validation", "schematron|"+e.Rule
		if e.Source != "" {
			it.Title += " (" + e.Source + ")"
			it.Generator = "schematron|" + e.Source + "#" + e.Rule
		}
	case qti.CodeInvalidXML, qti.CodeUnsupportedEncoding, qti.CodeUnsupportedXML:
		it.Title, it.Generator, it.Outcome = "XML parsing", "parse", OutcomeNameFatal
	case qti.CodeUnsupportedDocument:
		it.Title, it.Generator, it.Outcome = "Document type", "document-type", OutcomeNameFatal
	case qti.CodeInvalidZIP:
		it.Title, it.Generator = "Package", "package"
		if e.File == "" {
			it.Outcome = OutcomeNameFatal // the package itself cannot be read
		}
	case qti.CodeMissingManifest, qti.CodeUnsafePath, qti.CodeTooManyFiles:
		it.Title, it.Generator = "Package", "package"
	case qti.CodeUnsupportedVersion:
		it.Title, it.Generator = "QTI version", "version"
	case qti.CodeVersionOverridden:
		it.Title, it.Generator, it.Outcome = "QTI version", "version", OutcomeNameWarning
	case qti.CodeTooLarge, qti.CodeLimitExceeded:
		it.Title, it.Generator = "Limits", "limits"
	default:
		it.Title, it.Generator, it.Outcome = "Internal error", "internal", OutcomeNameException
	}
	return it
}

// schemaFor names the entry schema a document of the given schema name was
// validated against; source names a mounted XSD instead.
func schemaFor(schema, version, source string) string {
	if source != "" {
		return source
	}
	v, ok := qti.LookupVersion(version)
	if !ok {
		return ""
	}
	for _, t := range qti.DocumentTypes() {
		if t.Schema == schema {
			return v.SchemaFor(t)
		}
	}
	return v.ASI
}

// newReportID returns a random UUID (version 4).
func newReportID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
