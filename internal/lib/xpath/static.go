package xpath

import "slices"

// optimise is turned off only by tests, to compare the evaluation of
// expressions with and without the shortcuts that rely on this static
// analysis: steps that stop at their first node (step.first), paths looked
// up by an attribute (keyedPath), descendant steps by name from the index
// of the document (step.fromIndex), descendant steps from nested nodes,
// lookups in kept node-sets (Session.Constant) and descendant steps merged
// with their predicates (mergeDescendantSteps).
var optimise = true

// selectsFirst reports whether predicates end in [1] or [position() = 1]
// and the others filter regardless of position: each is of a type other
// than number, and calls neither position() nor last() in its own context.
// The step then selects the first node on the axis that passes the others.
func selectsFirst(preds []expr) bool {
	if len(preds) == 0 || !isPositionOne(preds[len(preds)-1]) {
		return false
	}
	for _, p := range preds[:len(preds)-1] {
		if !filtersByNode(p) {
			return false
		}
	}
	return true
}

// isPositionOne reports whether a predicate is the number 1, position() = 1
// or 1 = position().
func isPositionOne(e expr) bool {
	isOne := func(e expr) bool { n, ok := e.(numberExpr); return ok && n.n == 1 }
	if isOne(e) {
		return true
	}
	b, ok := e.(*binaryExpr)
	if !ok || b.op != "=" {
		return false
	}
	isPosition := func(e expr) bool { c, ok := e.(*callExpr); return ok && c.name == "position" }
	return isOne(b.left) && isPosition(b.right) || isPosition(b.left) && isOne(b.right)
}

// filtersByNode reports whether a predicate keeps a node or not whatever
// its proximity position and the context size: its value is never a
// number, which would be compared with the position, and it calls neither
// position() nor last().
func filtersByNode(e expr) bool {
	switch staticType(e) {
	case typeBoolean, typeString, typeNodeSet:
		return !usesPosition(e)
	}
	return false
}

type valueType uint8

const (
	typeUnknown valueType = iota
	typeBoolean
	typeNumber
	typeString
	typeNodeSet
)

// staticType returns the type every value of e has, or typeUnknown.
func staticType(e expr) valueType {
	switch x := e.(type) {
	case literalExpr:
		return typeString
	case numberExpr, *negateExpr:
		return typeNumber
	case *unionExpr, *filterExpr, *pathExpr:
		return typeNodeSet
	case *binaryExpr:
		switch x.op {
		case "or", "and", "=", "!=", "<", "<=", ">", ">=":
			return typeBoolean
		}
		return typeNumber
	case *callExpr:
		switch x.name {
		case "boolean", "not", "true", "false", "lang", "contains", "starts-with":
			return typeBoolean
		case "string", "concat", "substring-before", "substring-after", "substring",
			"normalize-space", "translate", "local-name", "namespace-uri", "name", "generate-id":
			return typeString
		case "id", "current":
			return typeNodeSet
		}
	}
	return typeUnknown // a variable, a number function, an extension function
}

// usesPosition reports whether e calls position() or last() in its own
// context. Predicates inside e have contexts of their own.
func usesPosition(e expr) bool {
	if c, ok := e.(*callExpr); ok && (c.name == "position" || c.name == "last") {
		return true
	}
	return slices.ContainsFunc(sameContext(e), usesPosition)
}

// sameContext returns the subexpressions of e that are evaluated in the
// context of e itself: all but the predicates.
func sameContext(e expr) []expr {
	switch x := e.(type) {
	case *filterExpr:
		return []expr{x.primary}
	case *pathExpr:
		if x.filter != nil {
			return []expr{x.filter}
		}
		return nil
	}
	return subexpressions(e)
}
