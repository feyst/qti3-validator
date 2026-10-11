package rules

import (
	"fmt"
	"strings"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xpath"
)

// The ids of the type checks, as docs/additional-checks.md lists them.
const (
	CheckValueType      = "value-type"
	CheckExpressionType = "expression-type"
	CheckAssignmentType = "assignment-type"
)

// valueType is the static type of a QTI value: its base type and
// cardinality. An empty field is unknown, and unknown types are never
// reported: the checks only report what is certain.
type valueType struct {
	base qti.BaseType
	card qti.Cardinality
}

func (t valueType) String() string {
	b, c := string(t.base), string(t.card)
	if b == "" {
		b = "unknown"
	}
	if c == "" {
		c = "unknown"
	}
	return "[" + b + ", " + c + "]"
}

// typeChecker checks the values and expressions of one document.
type typeChecker struct {
	vars     map[string]valueType
	findings []app.RuleFinding
	// room is the number of findings to keep, zero for all: the checks
	// stop once it is used up. Every finding is an error.
	room int
	// session is charged a step for every element the checks visit, so
	// they stop with the budget of the rules and with their context; err
	// is the error it stopped them with.
	session *xpath.Session
	err     error
	paths   *xpath.Paths
}

// checkTypes runs the type checks on a QTI document and returns the first
// room findings, all if room is zero. The work is charged to session; when
// that stops the checks, it returns the findings so far and the session's
// error. paths gives the findings their locations.
func checkTypes(session *xpath.Session, doc *xpath.Document, room int, paths *xpath.Paths) ([]app.RuleFinding, error) {
	root := firstElement(doc.Root)
	if root == nil || root.Space != qti.NamespaceASI {
		return nil, nil
	}
	c := &typeChecker{vars: builtinTypes(), room: room, session: session, paths: paths}
	for _, n := range root.Children {
		if n.Kind == xpath.ElementNode && isDeclaration(n.Local) {
			c.vars[attr(n, "identifier")] = valueType{qti.BaseType(attr(n, "base-type")), qti.Cardinality(attr(n, "cardinality"))}
		}
	}
	c.walk(root)
	return c.findings, c.err
}

// done reports whether the checks are over: the room for findings is used
// up, or the session stopped them.
func (c *typeChecker) done() bool {
	return c.err != nil || c.room > 0 && len(c.findings) >= c.room
}

// visit charges the session for an element the checks look at, and reports
// whether they go on.
func (c *typeChecker) visit() bool {
	if c.done() {
		return false
	}
	c.err = c.session.Charge(1)
	return c.err == nil
}

func builtinTypes() map[string]valueType {
	return map[string]valueType{
		"numAttempts":      {qti.Integer, qti.Single},
		"duration":         {qti.Duration, qti.Single},
		"completionStatus": {qti.Identifier, qti.Single},
		"QTI_CONTEXT":      {"", qti.Record},
	}
}

func isDeclaration(local string) bool {
	switch local {
	case "qti-response-declaration", "qti-outcome-declaration", "qti-template-declaration", "qti-context-declaration":
		return true
	}
	return false
}

// report adds a finding, unless the checks are over: the location and the
// message of a finding that is not kept are not worked out.
func (c *typeChecker) report(n *xpath.Node, check, format string, args ...any) {
	if c.done() {
		return
	}
	c.findings = append(c.findings, app.RuleFinding{Finding: qti.Finding{
		Code: qti.CodeValueType, Rule: check, Line: int(n.Line), Column: int(n.Column), Path: c.paths.Path(n),
		Message: fmt.Sprintf(format, args...),
	}})
}

// walk visits every element: declarations for their values, conditions and
// assignments for their expressions.
func (c *typeChecker) walk(n *xpath.Node) {
	for _, child := range n.Children {
		if child.Kind != xpath.ElementNode {
			continue
		}
		if !c.visit() {
			return
		}
		switch child.Local {
		case "qti-response-declaration", "qti-outcome-declaration", "qti-template-declaration", "qti-context-declaration":
			c.checkDeclarationValues(child)
		case "qti-response-if", "qti-response-else-if", "qti-outcome-if", "qti-outcome-else-if",
			"qti-template-if", "qti-template-else-if", "qti-branch-rule", "qti-pre-condition":
			if cond := firstElement(child); cond != nil {
				c.expect(cond, child.Local, valueType{qti.Boolean, qti.Single})
			}
		case "qti-set-outcome-value", "qti-set-template-value", "qti-set-correct-response",
			"qti-set-default-value", "qti-lookup-outcome-value":
			c.checkAssignment(child)
		}
		if !isExpression(child.Local) {
			c.walk(child)
		}
	}
}

// checkDeclarationValues checks the default, correct and mapped values of a
// declaration against its base type.
func (c *typeChecker) checkDeclarationValues(decl *xpath.Node) {
	base := qti.BaseType(attr(decl, "base-type"))
	id := attr(decl, "identifier")
	for _, part := range elements(decl) {
		switch part.Local {
		case "qti-default-value", "qti-correct-response":
			c.checkValues(part, base, id)
		case "qti-mapping":
			c.checkMapKeys(part, base, id)
		}
	}
}

// checkValues checks the qti-value elements of a default or correct value.
// A field of a record has its own base-type.
func (c *typeChecker) checkValues(part *xpath.Node, base qti.BaseType, id string) {
	for _, v := range part.Children {
		if v.Kind != xpath.ElementNode || v.Local != "qti-value" {
			continue
		}
		if !c.visit() {
			return
		}
		b := base
		if own := attr(v, "base-type"); own != "" {
			b = qti.BaseType(own)
		}
		if b != "" && !qti.ValidValue(b, v.StringValue()) {
			c.report(v, CheckValueType, "%s of %s: %q is not a valid %s value.",
				part.Local, id, strings.TrimSpace(v.StringValue()), b)
		}
	}
}

// checkMapKeys checks the keys of a qti-mapping against the response's base
// type.
func (c *typeChecker) checkMapKeys(mapping *xpath.Node, base qti.BaseType, id string) {
	if base == "" {
		return
	}
	for _, e := range mapping.Children {
		if e.Kind != xpath.ElementNode {
			continue
		}
		if !c.visit() {
			return
		}
		if e.Local == "qti-map-entry" && !qti.ValidValue(base, attr(e, "map-key")) {
			c.report(e, CheckValueType, "qti-mapping of %s: map-key %q is not a valid %s value.", id, attr(e, "map-key"), base)
		}
	}
}

// checkAssignment checks that the expression a rule assigns to a variable
// has the variable's type: "must result in a value with base-type and
// cardinality matching the declaration". An integer may be assigned to a
// float.
func (c *typeChecker) checkAssignment(rule *xpath.Node) {
	expr := firstElement(rule)
	if expr == nil {
		return
	}
	got := c.infer(expr)
	if rule.Local == "qti-lookup-outcome-value" {
		return // the expression is a lookup key, not the value
	}
	want, ok := c.vars[attr(rule, "identifier")]
	if !ok || want.card == qti.Record {
		return
	}
	baseOK := got.base == "" || want.base == "" || got.base == want.base || got.base == qti.Integer && want.base == qti.Float
	cardOK := got.card == "" || want.card == "" || got.card == want.card
	if !baseOK || !cardOK {
		c.report(expr, CheckAssignmentType, "%s sets %s %s to %s %s.",
			rule.Local, attr(rule, "identifier"), want, expr.Local, got)
	}
}

func isExpression(local string) bool {
	_, ok := operators[local]
	return ok
}

// expect checks that an expression has a type.
func (c *typeChecker) expect(expr *xpath.Node, context string, want valueType) {
	got := c.infer(expr)
	if (got.base != "" && got.base != want.base) || (got.card != "" && got.card != want.card) {
		c.report(expr, CheckExpressionType, "%s needs a %s value, but %s gives %s.", context, want, expr.Local, got)
	}
}
