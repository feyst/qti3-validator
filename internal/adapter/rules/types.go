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
}

// checkTypes runs the type checks on a QTI document.
func checkTypes(doc *xpath.Document) []app.RuleFinding {
	root := firstElement(doc.Root)
	if root == nil || root.Space != qti.NamespaceASI {
		return nil
	}
	c := &typeChecker{vars: builtinTypes()}
	for _, n := range root.Children {
		if n.Kind == xpath.ElementNode && isDeclaration(n.Local) {
			c.vars[attr(n, "identifier")] = valueType{qti.BaseType(attr(n, "base-type")), qti.Cardinality(attr(n, "cardinality"))}
		}
	}
	c.walk(root)
	return c.findings
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

func (c *typeChecker) report(n *xpath.Node, check, format string, args ...any) {
	c.findings = append(c.findings, app.RuleFinding{Finding: qti.Finding{
		Code: qti.CodeValueType, Rule: check, Line: n.Line, Column: n.Column, Path: n.Path(),
		Message: fmt.Sprintf(format, args...),
	}})
}

// walk visits every element: declarations for their values, conditions and
// assignments for their expressions.
func (c *typeChecker) walk(n *xpath.Node) {
	for _, child := range elements(n) {
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
	for _, v := range elements(part) {
		if v.Local != "qti-value" {
			continue
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
	for _, e := range elements(mapping) {
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
