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
//
// The others must also be unable to raise a type error (see mayFail). As
// written, they are evaluated on every node of the axis, so an error on a
// node after the first that passes is an error of the step; stopping at the
// first would return that node instead.
func selectsFirst(preds []expr) bool {
	if len(preds) == 0 || !isPositionOne(preds[len(preds)-1]) {
		return false
	}
	for _, p := range preds[:len(preds)-1] {
		if !filtersByNode(p) || mayFail(p) {
			return false
		}
	}
	return true
}

// nodeSetFunctions are the core functions whose argument must be a
// node-set.
var nodeSetFunctions = map[string]bool{
	"count": true, "sum": true, "local-name": true, "namespace-uri": true, "name": true, "generate-id": true,
}

// mayFail reports whether evaluating e, or an expression inside it, can
// raise a type error: a value that is not known to be a node-set is used
// where one is required, as the start of a path, the operand of a union,
// the primary of a filter or the argument of a node-set function. That is
// a variable, as in $v/x, or an expression of another type. An extension
// function checks its arguments itself and is taken to be able to fail.
//
// Whether an expression fails this way can depend on the context node:
// @x or $s/x fails only where there is no @x. Running out of steps and a
// cancelled session are not counted; they stop an evaluation anywhere.
//
// Nor is a variable without a value. Compile rejects a reference to a
// variable that Options.Variables does not declare, so that error needs a
// caller that declares a variable and then leaves it out of
// Context.Variables. The schematron engine cannot: it declares a variable
// where it compiles the let that binds it, and binds every let of a schema,
// pattern or rule before it evaluates an expression in its scope. Counting
// it would take the shortcut from every predicate that compares with a
// variable, such as [@id = $v][1].
func mayFail(e expr) bool {
	needsNodeSet := func(sub expr) bool { return staticType(sub) != typeNodeSet }
	switch x := e.(type) {
	case *unionExpr:
		if needsNodeSet(x.left) || needsNodeSet(x.right) {
			return true
		}
	case *filterExpr:
		if needsNodeSet(x.primary) {
			return true
		}
	case *pathExpr:
		if x.filter != nil && needsNodeSet(x.filter) {
			return true
		}
	case *callExpr:
		if _, core := functions[x.name]; !core {
			return true
		}
		if nodeSetFunctions[x.name] && len(x.args) > 0 && needsNodeSet(x.args[0]) {
			return true
		}
	}
	return slices.ContainsFunc(subexpressions(e), mayFail)
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
