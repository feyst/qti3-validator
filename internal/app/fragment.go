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
//     validated inside the elements that allow it (fragmentContexts): those
//     that are like the places where the documents of the package include
//     it, which are in included, and otherwise all of them (checkInContexts);
//   - a root that none of those elements allows is not checked, which a
//     warning says. docs/report.md lists both kinds.
//
// An IDREF without its ID is no error: the ID may be in the document that
// includes the fragment. XIncludes are not resolved, and the Schematron
// rules do not run: they need the whole document.
func (v *Validator) validateFragment(ctx context.Context, data []byte, req qti.VersionRequest, limits qti.Limits,
	included []qti.Inclusion,
) qti.DocumentResult {
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
		allowing := v.fragments[version].contextsFor(profile.Schema, root.Local)
		if len(allowing) == 0 {
			res = fragmentNotChecked(docType, root.Local)
			break
		}
		f := fragment{schema: profile.Schema, docType: docType, data: data, root: root.Local, at: at, limits: limits}
		res = f.checkInContexts(ctx, allowing, placesOf(allowing, included, elements.ManyTyped), elements.ManyTyped)
	}
	res.Version = version
	return res
}

// fragmentPlaces are the places where the documents of a package include a
// fragment; none for a fragment that no document includes.
type fragmentPlaces struct{ included []qti.Inclusion }

// A fragment is a fragment whose root is not a global element of the schema.
type fragment struct {
	schema  SchemaChecker
	docType qti.DocumentType
	data    []byte
	root    string
	at      position // of the root
	limits  qti.Limits
}

// A place is where a fragment must be valid: in one of its contexts. An
// exact place is like the places where documents include the fragment; the
// other one, which comes last, stands for the places the validator knows no
// context for, and for a fragment that no document includes: its contexts
// are all those that allow the root.
type place struct {
	contexts  []int // indexes in fragmentContexts
	exact     bool
	documents []string // that include the fragment there, once each
}

// placesOf returns the places where a fragment must be valid: one for every
// set of contexts that the places where it is included select, among the
// contexts that allow its root.
func placesOf(allowing []int, included []qti.Inclusion, manyTyped map[string]bool) []place {
	var exact []place
	other := place{contexts: allowing}
	inexact := len(included) == 0
	for _, in := range included {
		within := strings.Split(strings.TrimPrefix(in.Within, "/"), "/")
		var like []int
		for _, i := range allowing {
			if fragmentContexts[i].like(within, manyTyped) {
				like = append(like, i)
			}
		}
		at := &other
		if len(like) == 0 {
			inexact = true
		} else {
			n := slices.IndexFunc(exact, func(p place) bool { return slices.Equal(p.contexts, like) })
			if n < 0 {
				exact = append(exact, place{contexts: like, exact: true})
				n = len(exact) - 1
			}
			at = &exact[n]
		}
		// The places come by document, so one that is there twice is the last.
		if n := len(at.documents); n == 0 || at.documents[n-1] != in.Document {
			at.documents = append(at.documents, in.Document)
		}
	}
	if inexact {
		return append(exact, other)
	}
	return exact
}

// checkInContexts validates a fragment in every place where it must be
// valid. In a place it is valid when one of the contexts of the place
// accepts it; when none does, the errors are those of the context with the
// fewest. So a fragment that documents include must be valid wherever it is
// included, and one whose place is not known is an error only when it is
// valid nowhere.
//
// The errors of a root that the schema declares with more than one type
// say in which element they were found, as another element gives other
// errors (note).
func (f fragment) checkInContexts(ctx context.Context, allowing []int, places []place, manyTyped map[string]bool) qti.DocumentResult {
	results := map[int]qti.DocumentResult{} // of the contexts tried so far
	valid := -1                             // a context that accepts the fragment
	var failed []int                        // the contexts whose errors are reported, in the order of the places
	notes := map[int]string{}
	for _, p := range places {
		i, invalid := f.checkInPlace(ctx, p, results)
		switch _, reported := notes[i]; {
		case !invalid && results[i].Outcome != qti.OutcomeValid:
			return results[i] // stopped, or the fragment cannot be read in a context
		case !invalid:
			valid = i
		case !reported:
			failed = append(failed, i)
			notes[i] = f.note(fragmentContexts[i], p, len(allowing), manyTyped)
		}
	}
	if len(failed) == 0 {
		return results[valid]
	}
	out := results[failed[0]]
	out.Errors = nil
	for _, i := range failed {
		for _, e := range results[i].Errors {
			e.Message += notes[i]
			out.Errors = append(out.Errors, e)
		}
	}
	if limit := f.limits.MaxErrors; limit > 0 && len(out.Errors) > limit {
		out.Errors = out.Errors[:limit]
	}
	return out
}

// checkInPlace validates a fragment in the contexts of a place, each of
// which is validated once for all places. It returns the context that
// decides: the first that accepts the fragment, or that could not validate
// it. When they all find errors, invalid is true, and the context is the
// first of those with the fewest errors.
func (f fragment) checkInPlace(ctx context.Context, p place, results map[int]qti.DocumentResult) (decides int, invalid bool) {
	best := -1
	for _, i := range p.contexts {
		res, tried := results[i]
		if !tried {
			res = fragmentContexts[i].check(ctx, f.schema, f.docType, f.data, f.at, f.limits)
			results[i] = res
		}
		if res.Outcome != qti.OutcomeInvalid {
			return i, false
		}
		if best < 0 || len(res.Errors) < len(results[best].Errors) {
			best = i
		}
	}
	return best, true
}

// note is what the errors of a fragment say after their message: in which
// element they were found, and why there. Only a root that is another type
// in another element has one: any other root has the same errors wherever
// it stands.
func (f fragment) note(c fragmentContext, p place, allowing int, manyTyped map[string]bool) string {
	if allowing == 1 || !manyTyped[f.root] {
		return ""
	}
	in := c.name(manyTyped)
	if !p.exact {
		return fmt.Sprintf(" (checked as a child of %s: the fragment is valid in none of the %d places the validator knows for a %s, "+
			"and has the fewest errors in this one)", in, allowing, f.root)
	}
	docs, verb := p.documents, "include"
	switch n := len(docs); {
	case n == 1:
		verb = "includes"
	case n > 3:
		docs = append(slices.Clone(docs[:3]), fmt.Sprintf("%d other documents", n-3))
	}
	names := docs[0]
	if n := len(docs); n > 1 {
		names = strings.Join(docs[:n-1], ", ") + " and " + docs[n-1]
	}
	return fmt.Sprintf(" (checked as a child of %s, where %s %s the fragment)", in, names, verb)
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
	path        string   // of the element the fragment's root becomes a child of
	elements    []string // of that path
	depth       int      // of that element
}

const (
	contextItem    = `<c:qti-assessment-item xmlns:c="` + qti.NamespaceASI + `" identifier="fragment" title="fragment" adaptive="false" time-dependent="false">`
	contextItemEnd = `</c:qti-assessment-item>`
	contextTest    = `<c:qti-assessment-test xmlns:c="` + qti.NamespaceASI + `" identifier="fragment" title="fragment">`
	contextTestEnd = `</c:qti-assessment-test>`
	contextBody    = contextItem + `<c:qti-item-body>`
	contextBodyEnd = `</c:qti-item-body>` + contextItemEnd
	contextOutcome = contextItem + `<c:qti-outcome-declaration identifier="OUTCOME" cardinality="single" base-type="float">`
	contextPart    = `<c:qti-test-part identifier="part" navigation-mode="linear" submission-mode="individual">`
	contextSection = `<c:qti-assessment-section identifier="section" title="section" visible="true">`

	contextFeedbackBlock = `<c:qti-feedback-block outcome-identifier="FEEDBACK" identifier="feedback" show-hide="show">`
	contextRubricBlock   = `<c:qti-rubric-block view="candidate" use="instructions">`
	contextTemplateBlock = `<c:qti-template-block template-identifier="TEMPLATE" identifier="template" show-hide="show">`
	contextSectionRoot   = `<c:qti-assessment-section xmlns:c="` + qti.NamespaceASI + `" identifier="section" title="section" visible="true">`
)

// fragmentContexts are the places a fragment is validated in: every one
// that allows its root is one it may stand in. They are tried in this
// order, which matters only for the errors of a fragment that is valid in
// none of them: of the contexts with the fewest errors, the first one is
// reported. The rules of response, outcome and template processing come
// first, then the content of an item body, then the parts of items, tests
// and interactions.
//
// Most elements have one type wherever they stand. The ones the schema
// declares with more than one type (SchemaElements.ManyTyped) are another
// type in another context, so those have a context for each of their types:
// qti-rubric-block, qti-content-body, qti-feedback-block, qti-template-block
// and qti-template-inline.
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
	// qti-rubric-block: the rubric block of a test, which is another type
	// than the one of an item body above.
	{open: contextSectionRoot, close: `</c:qti-assessment-section>`},
	// The parts of a test: those before its test parts, a test part, and
	// those after them.
	{open: contextTest, close: contextPart + contextSection + `</c:qti-assessment-section></c:qti-test-part>` + contextTestEnd},
	{open: contextTest, close: contextTestEnd},
	{open: contextTest + contextPart + contextSection + `</c:qti-assessment-section></c:qti-test-part>`, close: contextTestEnd},
	// The parts of a test part, such as qti-item-session-control.
	{open: contextTest + contextPart, close: contextSection + `</c:qti-assessment-section></c:qti-test-part>` + contextTestEnd},
	// The choices of the other interactions: qti-simple-associable-choice,
	// qti-simple-match-set (the first of the two a match interaction has),
	// qti-gap-text and qti-gap-img, qti-inline-choice, qti-hotspot-choice
	// and qti-associable-hotspot.
	{
		open:  contextBody + `<c:qti-associate-interaction response-identifier="RESPONSE">`,
		close: `</c:qti-associate-interaction>` + contextBodyEnd,
	},
	{
		open: contextBody + `<c:qti-match-interaction response-identifier="RESPONSE">`,
		close: `<c:qti-simple-match-set><c:qti-simple-associable-choice identifier="choice" match-max="1"/></c:qti-simple-match-set>` +
			`</c:qti-match-interaction>` + contextBodyEnd,
	},
	{
		open:  contextBody + `<c:qti-gap-match-interaction response-identifier="RESPONSE">`,
		close: `<c:p><c:qti-gap identifier="gap"/></c:p></c:qti-gap-match-interaction>` + contextBodyEnd,
	},
	{
		open:  contextBody + `<c:p><c:qti-inline-choice-interaction response-identifier="RESPONSE">`,
		close: `</c:qti-inline-choice-interaction></c:p>` + contextBodyEnd,
	},
	{
		open:  contextBody + `<c:qti-hotspot-interaction response-identifier="RESPONSE"><c:img src="image.png" alt=""/>`,
		close: `</c:qti-hotspot-interaction>` + contextBodyEnd,
	},
	{
		open:  contextBody + `<c:qti-graphic-associate-interaction response-identifier="RESPONSE"><c:img src="image.png" alt=""/>`,
		close: `</c:qti-graphic-associate-interaction>` + contextBodyEnd,
	},
	// The content of modal feedback: a qti-content-body. Those of the other
	// elements that have one are other types, and come last.
	{
		open:  contextItem + `<c:qti-modal-feedback outcome-identifier="FEEDBACK" identifier="feedback" show-hide="show">`,
		close: `</c:qti-modal-feedback>` + contextItemEnd,
	},
	// The values of a declaration: qti-value, qti-map-entry and
	// qti-area-map-entry.
	{
		open:  contextOutcome + `<c:qti-default-value>`,
		close: `</c:qti-default-value></c:qti-outcome-declaration>` + contextItemEnd,
	},
	{
		open:  contextItem + `<c:qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="string"><c:qti-mapping>`,
		close: `</c:qti-mapping></c:qti-response-declaration>` + contextItemEnd,
	},
	{
		open:  contextItem + `<c:qti-response-declaration identifier="RESPONSE" cardinality="single" base-type="point"><c:qti-area-mapping>`,
		close: `</c:qti-area-mapping></c:qti-response-declaration>` + contextItemEnd,
	},
	// The lookup tables of an outcome declaration, and their entries.
	{open: contextOutcome, close: `</c:qti-outcome-declaration>` + contextItemEnd},
	{open: contextOutcome + `<c:qti-match-table>`, close: `</c:qti-match-table></c:qti-outcome-declaration>` + contextItemEnd},
	{open: contextOutcome + `<c:qti-interpolation-table>`, close: `</c:qti-interpolation-table></c:qti-outcome-declaration>` + contextItemEnd},
	// The parts of an item reference: qti-weight, qti-variable-mapping and
	// qti-template-default.
	{
		open:  contextSectionRoot + `<c:qti-assessment-item-ref identifier="item" href="item.xml">`,
		close: `</c:qti-assessment-item-ref></c:qti-assessment-section>`,
	},
	// The qti-content-body of the other elements that have one, each its
	// own type: of a feedback block, a rubric block and a template block of
	// an item, of a template block in a rubric block, of the feedback of a
	// test, and of the rubric block of a test.
	{open: contextBody + contextFeedbackBlock, close: `</c:qti-feedback-block>` + contextBodyEnd},
	{open: contextBody + contextRubricBlock, close: `</c:qti-rubric-block>` + contextBodyEnd},
	{open: contextBody + contextTemplateBlock, close: `</c:qti-template-block>` + contextBodyEnd},
	{
		open:  contextBody + contextRubricBlock + `<c:qti-content-body>` + contextTemplateBlock,
		close: `</c:qti-template-block></c:qti-content-body></c:qti-rubric-block>` + contextBodyEnd,
	},
	{
		open: contextTest + contextPart + contextSection + `</c:qti-assessment-section></c:qti-test-part>` +
			`<c:qti-test-feedback access="atEnd" outcome-identifier="FEEDBACK" identifier="feedback" show-hide="show">`,
		close: `</c:qti-test-feedback>` + contextTestEnd,
	},
	{open: contextSectionRoot + `<c:qti-rubric-block view="candidate">`, close: `</c:qti-rubric-block></c:qti-assessment-section>`},
	// The content of the rubric block of an item, where a qti-template-block
	// and a qti-template-inline are other types than elsewhere, and that of
	// a template block, where a qti-feedback-block is.
	{
		open:  contextBody + contextRubricBlock + `<c:qti-content-body>`,
		close: `</c:qti-content-body></c:qti-rubric-block>` + contextBodyEnd,
	},
	{
		open:  contextBody + contextTemplateBlock + `<c:qti-content-body>`,
		close: `</c:qti-content-body></c:qti-template-block>` + contextBodyEnd,
	},
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
		c.path, c.elements, c.depth = "/"+strings.Join(open, "/"), open, len(open)
	}
}

// like reports whether an element with the path within is the same place as
// the context: an element found there has the type it has in the context.
// The type of an element follows from the element it stands in, and when
// the schema declares that one with more than one type, from the one around
// it, and so on. So the two are alike when their innermost elements have
// the same names, up to and including one that has a single type.
func (c fragmentContext) like(within []string, manyTyped map[string]bool) bool {
	for i := 1; i <= len(c.elements) && i <= len(within); i++ {
		name := c.elements[len(c.elements)-i]
		if within[len(within)-i] != name {
			return false
		}
		if !manyTyped[name] {
			return true
		}
	}
	return false
}

// name names the context in a message, by as many of its innermost elements
// as say which type it is: "qti-content-body of qti-rubric-block of
// qti-item-body".
func (c fragmentContext) name(manyTyped map[string]bool) string {
	n := len(c.elements) - 1
	for n > 0 && manyTyped[c.elements[n]] {
		n--
	}
	names := slices.Clone(c.elements[n:])
	slices.Reverse(names)
	return strings.Join(names, " of ")
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

// contextCache remembers, for one QTI version, the contexts of every root
// element a fragment had so far.
type contextCache struct {
	mu     sync.Mutex
	byRoot map[string][]int // indexes in fragmentContexts, in its order
}

// contextsFor returns the contexts that allow a fragment with the given
// root, an element the schema declares locally: all of them, as an element
// may be another type in another one. It returns none when no context
// allows the root.
func (cc *contextCache) contextsFor(schema SchemaChecker, root string) []int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	allowing, known := cc.byRoot[root]
	if !known {
		for i, c := range fragmentContexts {
			if c.allows(schema, root) {
				allowing = append(allowing, i)
			}
		}
		cc.byRoot[root] = allowing
	}
	return allowing
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
