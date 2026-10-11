package app

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xmldoc"
)

// unsupportedDocument reports whether a document was well-formed but its
// root is not a document type the validator knows.
func unsupportedDocument(res qti.DocumentResult) bool {
	return len(res.Errors) == 1 && res.Errors[0].Code == qti.CodeUnsupportedDocument
}

// validateFragment validates a QTI fragment of a package: XML that a QTI
// document includes with XInclude. data is well-formed. Its root is
// ordinary QTI content, such as a div, so it must be an element of the QTI
// schema:
//
//   - a root the schema declares nowhere is an error;
//   - a root the schema declares as a global element, as it does for the
//     HTML content elements, is validated against it;
//   - any other root is valid only inside some other element, so it is
//     validated inside one that allows it (fragmentContexts);
//   - a root that none of those elements allows is not checked, which a
//     warning says.
//
// An IDREF without its ID is no error: the ID may be in the document that
// includes the fragment. XIncludes are not resolved, and the Schematron
// rules do not run: they need the whole document.
func (v *Validator) validateFragment(ctx context.Context, data []byte, req qti.VersionRequest, limits qti.Limits) qti.DocumentResult {
	root, at, err := rootStart(data)
	if err != nil {
		return detectionFailure(err)
	}
	if root.Space != qti.NamespaceASI {
		return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedDocument, fmt.Sprintf(
			"root element {%s}%s of a QTI fragment is not in the QTI namespace %s", root.Space, root.Local, qti.NamespaceASI))
	}
	version := qti.ChooseVersion(req, qti.DeclaredVersion{}).Version.Name
	profile := v.versions[version]
	docType := qti.DocumentType{Namespace: root.Space, Root: root.Local, Schema: qti.SchemaFragment}

	var res qti.DocumentResult
	switch elements := profile.Elements; {
	case elements != nil && !elements.Declares(root.Local):
		res = qti.DocumentResult{Schema: docType.Schema, Outcome: qti.OutcomeInvalid, Ran: qti.Checks{Schema: true}, Errors: []qti.Finding{{
			Code: qti.CodeValidation, Line: at.line, Column: at.column, Path: "/" + root.Local,
			Message: fmt.Sprintf("root element %s of a QTI fragment is not an element of QTI %s", root.Local, version),
		}}}
	case elements == nil || elements.Global[root.Local]:
		var verdict SchemaVerdict
		res, verdict = checkSchema(ctx, profile.Schema, docType, data, limits)
		res = withoutUnresolvedIDRefs(res, verdict)
		if verdict.RootUndeclared && res.Outcome == qti.OutcomeInvalid {
			res = fragmentNotChecked(docType, root.Local)
		}
	default:
		c, ok := v.fragments[version].contextFor(profile.Schema, root.Local)
		if !ok {
			res = fragmentNotChecked(docType, root.Local)
			break
		}
		res = c.check(ctx, profile.Schema, docType, data, at, limits)
	}
	res.Version = version
	return res
}

// fragmentNotChecked is the result of a fragment whose root is an element
// of QTI, but one the validator cannot check on its own.
func fragmentNotChecked(docType qti.DocumentType, root string) qti.DocumentResult {
	return qti.DocumentResult{Valid: true, Schema: docType.Schema, Outcome: qti.OutcomeValid, Warnings: []qti.Finding{{
		Code: qti.CodeNotChecked, Path: "/" + root,
		Message: fmt.Sprintf("the content of this QTI fragment was not checked: its root element %s is valid only where "+
			"it is included, and the validator cannot check it apart from that document", root),
	}}}
}

// withoutUnresolvedIDRefs drops the IDREFs that do not resolve inside a
// fragment from a schema result.
func withoutUnresolvedIDRefs(res qti.DocumentResult, verdict SchemaVerdict) qti.DocumentResult {
	if len(verdict.UnresolvedIDRefs) == 0 || len(res.Errors) < len(verdict.Findings) {
		return res
	}
	kept := make([]qti.Finding, 0, len(res.Errors))
	for i, e := range res.Errors {
		if i >= len(verdict.Findings) || !slices.Contains(verdict.UnresolvedIDRefs, i) {
			kept = append(kept, e)
		}
	}
	res.Errors = kept
	if len(kept) == 0 && res.Outcome == qti.OutcomeInvalid {
		res.Valid, res.Outcome = true, qti.OutcomeValid
	}
	return res
}

// position is where the root element of a document starts.
type position struct{ offset, line, column int }

// rootStart returns the name of the root element and where its start tag
// begins.
func rootStart(data []byte) (xml.Name, position, error) {
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
	if err != nil {
		return xml.Name{}, position{}, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	// DetectRoot accepted the encoding: UTF-8, or its subset US-ASCII.
	dec.CharsetReader = func(_ string, input io.Reader) (io.Reader, error) { return input, nil }
	for {
		at := position{offset: int(dec.InputOffset())}
		at.line, at.column = dec.InputPos()
		tok, err := dec.RawToken()
		if err != nil {
			return xml.Name{}, position{}, err
		}
		if _, ok := tok.(xml.StartElement); ok {
			return root, at, nil
		}
	}
}

// contextPrefix is the namespace prefix of the elements put around a
// fragment. A prefix leaves the namespaces of the fragment's own elements
// as they are, which a default namespace would not.
const contextPrefix = "qti3-validator-context"

// A fragmentContext is a place in a QTI document where content may stand: a
// fragment whose root is not a global element of the schema is validated
// there. The elements are written with the prefix c, which stands for
// contextPrefix, and open has no line break, so that the lines of the
// fragment keep their numbers.
type fragmentContext struct {
	open, close string
	path        string // of the element the fragment's root becomes a child of
	depth       int    // of that element
}

const (
	contextItem    = `<c:qti-assessment-item xmlns:c="` + qti.NamespaceASI + `" identifier="fragment" title="fragment" adaptive="false" time-dependent="false">`
	contextItemEnd = `</c:qti-assessment-item>`
	contextTest    = `<c:qti-assessment-test xmlns:c="` + qti.NamespaceASI + `" identifier="fragment" title="fragment">`
	contextTestEnd = `</c:qti-assessment-test>`
	contextPart    = `<c:qti-test-part identifier="part" navigation-mode="linear" submission-mode="individual">`
	contextSection = `<c:qti-assessment-section identifier="section" title="section" visible="true">`
)

// fragmentContexts are tried in this order; the first that allows the root
// of a fragment is the one it is validated in. The rules of response,
// outcome and template processing come first, then the content of an item
// body, then the parts of items, tests and interactions.
var fragmentContexts = []fragmentContext{
	// Outcome rules, such as qti-outcome-condition.
	{open: `<c:qti-outcome-processing xmlns:c="` + qti.NamespaceASI + `">`, close: `</c:qti-outcome-processing>`},
	// Response rules, such as qti-response-condition.
	{open: contextItem + `<c:qti-response-processing>`, close: `</c:qti-response-processing>` + contextItemEnd},
	// Template rules, such as qti-template-condition.
	{open: contextItem + `<c:qti-template-processing>`, close: `</c:qti-template-processing>` + contextItemEnd},
	// Block content, such as an interaction or a qti-feedback-block.
	{open: contextItem + `<c:qti-item-body>`, close: `</c:qti-item-body>` + contextItemEnd},
	{open: `<c:div xmlns:c="` + qti.NamespaceASI + `">`, close: `</c:div>`},
	// Inline content, such as a qti-text-entry-interaction.
	{open: `<c:p xmlns:c="` + qti.NamespaceASI + `">`, close: `</c:p>`},
	// Expressions, such as qti-match.
	{
		open:  contextItem + `<c:qti-response-processing><c:qti-response-condition><c:qti-response-if>`,
		close: `</c:qti-response-if></c:qti-response-condition></c:qti-response-processing>` + contextItemEnd,
	},
	// The branches of a condition: the if, and what follows one.
	{open: contextItem + `<c:qti-response-processing><c:qti-response-condition>`, close: `</c:qti-response-condition></c:qti-response-processing>` + contextItemEnd},
	{
		open:  contextItem + `<c:qti-response-processing><c:qti-response-condition><c:qti-response-if><c:qti-null/></c:qti-response-if>`,
		close: `</c:qti-response-condition></c:qti-response-processing>` + contextItemEnd,
	},
	{open: `<c:qti-outcome-processing xmlns:c="` + qti.NamespaceASI + `"><c:qti-outcome-condition>`, close: `</c:qti-outcome-condition></c:qti-outcome-processing>`},
	{
		open:  `<c:qti-outcome-processing xmlns:c="` + qti.NamespaceASI + `"><c:qti-outcome-condition><c:qti-outcome-if><c:qti-null/></c:qti-outcome-if>`,
		close: `</c:qti-outcome-condition></c:qti-outcome-processing>`,
	},
	{open: contextItem + `<c:qti-template-processing><c:qti-template-condition>`, close: `</c:qti-template-condition></c:qti-template-processing>` + contextItemEnd},
	{
		open:  contextItem + `<c:qti-template-processing><c:qti-template-condition><c:qti-template-if><c:qti-null/></c:qti-template-if>`,
		close: `</c:qti-template-condition></c:qti-template-processing>` + contextItemEnd,
	},
	// The parts of an item, such as qti-response-declaration and
	// qti-item-body.
	{open: contextItem, close: contextItemEnd},
	// The parts of a declaration, such as qti-correct-response and
	// qti-mapping.
	{
		open:  contextItem + `<c:qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="string">`,
		close: `</c:qti-response-declaration>` + contextItemEnd,
	},
	// The parts of a choice interaction: qti-prompt and qti-simple-choice.
	{
		open:  contextItem + `<c:qti-item-body><c:qti-choice-interaction response-identifier="RESPONSE">`,
		close: `<c:qti-simple-choice identifier="choice"/></c:qti-choice-interaction></c:qti-item-body>` + contextItemEnd,
	},
	// The body of a stimulus.
	{
		open:  `<c:qti-assessment-stimulus xmlns:c="` + qti.NamespaceASI + `" identifier="fragment" title="fragment">`,
		close: `</c:qti-assessment-stimulus>`,
	},
	// The parts of a section, such as qti-assessment-item-ref and
	// qti-rubric-block.
	{open: `<c:qti-assessment-section xmlns:c="` + qti.NamespaceASI + `" identifier="section" title="section" visible="true">`, close: `</c:qti-assessment-section>`},
	// The parts of a test: those before its test parts, a test part, and
	// those after them.
	{open: contextTest, close: contextPart + contextSection + `</c:qti-assessment-section></c:qti-test-part>` + contextTestEnd},
	{open: contextTest, close: contextTestEnd},
	{open: contextTest + contextPart + contextSection + `</c:qti-assessment-section></c:qti-test-part>`, close: contextTestEnd},
	// The parts of a test part, such as qti-item-session-control.
	{open: contextTest + contextPart, close: contextSection + `</c:qti-assessment-section></c:qti-test-part>` + contextTestEnd},
}

func init() {
	for i := range fragmentContexts {
		c := &fragmentContexts[i]
		c.open = strings.ReplaceAll(c.open, "c:", contextPrefix+":")
		c.open = strings.ReplaceAll(c.open, "xmlns:c=", "xmlns:"+contextPrefix+"=")
		c.close = strings.ReplaceAll(c.close, "c:", contextPrefix+":")
		// The path is that of the elements open leaves open.
		dec := xml.NewDecoder(strings.NewReader(c.open))
		var open []string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				open = append(open, t.Name.Local)
			case xml.EndElement:
				open = open[:len(open)-1]
			}
		}
		c.path, c.depth = "/"+strings.Join(open, "/"), len(open)
	}
}

// wrap puts the context around the root element of a document, which
// starts at offset.
func (c fragmentContext) wrap(data []byte, offset int) []byte {
	out := make([]byte, 0, len(data)+len(c.open)+len(c.close))
	out = append(out, data[:offset]...)
	out = append(out, c.open...)
	out = append(out, data[offset:]...)
	return append(out, c.close...)
}

// within widens the limits by what the context adds to a document.
func (c fragmentContext) within(limits qti.Limits) qti.Limits {
	limits.MaxDocumentSize += int64(len(c.open) + len(c.close))
	limits.MaxDepth += c.depth
	return limits
}

// check validates a fragment inside the context, and reports the findings
// as findings in the fragment: with the paths and the columns it has
// without the context.
func (c fragmentContext) check(ctx context.Context, schema SchemaChecker, docType qti.DocumentType,
	data []byte, at position, limits qti.Limits,
) qti.DocumentResult {
	res, verdict := checkSchema(ctx, schema, docType, c.wrap(data, at.offset), c.within(limits))
	res = withoutUnresolvedIDRefs(res, verdict)
	for i := range res.Errors {
		e := &res.Errors[i]
		if rest, ok := strings.CutPrefix(e.Path, c.path+"/"); ok {
			e.Path = "/" + rest
		}
		if e.Line == at.line && e.Column >= at.column+len(c.open) {
			e.Column -= len(c.open)
		}
	}
	return res
}

// allows reports whether the context allows an element named root: an
// empty one has no findings but its own, which are below the context.
func (c fragmentContext) allows(schema SchemaChecker, root string) bool {
	probe := c.open + `<` + root + ` xmlns="` + qti.NamespaceASI + `"/>` + c.close
	verdict := schema.CheckSchema([]byte(probe), qti.DefaultLimits)
	if verdict.Outcome != qti.OutcomeValid && verdict.Outcome != qti.OutcomeInvalid {
		return false
	}
	own := c.path + "/" + root
	for i, f := range verdict.Findings {
		if slices.Contains(verdict.UnresolvedIDRefs, i) {
			continue
		}
		if f.Path != own && !strings.HasPrefix(f.Path, own+"/") {
			return false
		}
	}
	return true
}

// contextCache remembers, for one QTI version, the context of every root
// element a fragment had so far.
type contextCache struct {
	mu     sync.Mutex
	byRoot map[string]int // an index in fragmentContexts, or -1 for none
}

// contextFor returns the context a fragment with the given root, an element
// the schema declares locally, is validated in. ok is false when no context
// allows it.
func (cc *contextCache) contextFor(schema SchemaChecker, root string) (c fragmentContext, ok bool) {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	i, known := cc.byRoot[root]
	if !known {
		i = slices.IndexFunc(fragmentContexts, func(c fragmentContext) bool { return c.allows(schema, root) })
		cc.byRoot[root] = i
	}
	if i < 0 {
		return fragmentContext{}, false
	}
	return fragmentContexts[i], true
}

// requireManifest makes the package's imsmanifest.xml an error when it is
// readable but not a QTI 3 content package manifest, such as a manifest in
// the namespace of plain IMS Content Packaging, or a QTI item. Its result
// replaces the one of the document it is instead, so the report does not
// count checks of a document that is in the wrong place. A manifest of a
// document type that a mounted validator adds is that validator's to judge.
func (v *Validator) requireManifest(data []byte, res qti.DocumentResult) qti.DocumentResult {
	if res.Schema == qti.SchemaManifest || res.Schema == "" && !unsupportedDocument(res) {
		return res // a QTI 3 manifest, or one that could not be read: its own errors say so
	}
	root, err := xmldoc.DetectRoot(bytes.NewReader(data))
	if err != nil {
		return res
	}
	if _, custom := v.custom[root]; custom {
		return res
	}
	return qti.Failure(qti.OutcomeInvalid, "", qti.CodeUnsupportedDocument, fmt.Sprintf(
		"%s is not a QTI 3 content package manifest: its root element is {%s}%s, not {%s}manifest",
		qti.ManifestName, root.Space, root.Local, qti.NamespaceManifest))
}
