package xpath

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

// Value is the result of an expression: bool, float64, string or NodeSet.
type Value any

// NodeSet is a set of nodes in document order without duplicates.
type NodeSet []*Node

// Context is the dynamic context of an evaluation.
type Context struct {
	// Node is the context node; current() returns it as well.
	Node *Node
	// Variables holds the values of the variables declared at compile time.
	Variables map[string]Value
}

// Evaluate evaluates the expression in ctx.
func (e *Expr) Evaluate(ctx Context) (Value, error) {
	ev := &evalCtx{node: ctx.Node, pos: 1, size: 1, current: ctx.Node, vars: ctx.Variables}
	v, err := e.root.eval(ev)
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", e.source, err)
	}
	return v, nil
}

// EvaluateBool evaluates the expression and converts the result with boolean().
func (e *Expr) EvaluateBool(ctx Context) (bool, error) {
	v, err := e.Evaluate(ctx)
	if err != nil {
		return false, err
	}
	return toBool(v), nil
}

// EvaluateString evaluates the expression and converts the result with string().
func (e *Expr) EvaluateString(ctx Context) (string, error) {
	v, err := e.Evaluate(ctx)
	if err != nil {
		return "", err
	}
	return toString(v), nil
}

// Select evaluates an expression that must return a node-set.
func (e *Expr) Select(ctx Context) (NodeSet, error) {
	v, err := e.Evaluate(ctx)
	if err != nil {
		return nil, err
	}
	ns, ok := v.(NodeSet)
	if !ok {
		return nil, fmt.Errorf("xpath %q: result is not a node-set", e.source)
	}
	return ns, nil
}

type evalCtx struct {
	node      *Node
	pos, size int
	current   *Node
	vars      map[string]Value
}

func (c *evalCtx) with(n *Node, pos, size int) *evalCtx {
	return &evalCtx{node: n, pos: pos, size: size, current: c.current, vars: c.vars}
}

type expr interface {
	eval(*evalCtx) (Value, error)
}

type literalExpr struct{ s string }
type numberExpr struct{ n float64 }
type variableExpr struct{ name string }

func (e literalExpr) eval(*evalCtx) (Value, error) { return e.s, nil }
func (e numberExpr) eval(*evalCtx) (Value, error)  { return e.n, nil }
func (e variableExpr) eval(c *evalCtx) (Value, error) {
	v, ok := c.vars[e.name]
	if !ok {
		return nil, fmt.Errorf("variable $%s has no value", e.name)
	}
	return v, nil
}

type negateExpr struct{ e expr }

func (e *negateExpr) eval(c *evalCtx) (Value, error) {
	v, err := e.e.eval(c)
	if err != nil {
		return nil, err
	}
	return -toNumber(v), nil
}

type unionExpr struct{ left, right expr }

func (e *unionExpr) eval(c *evalCtx) (Value, error) {
	l, err := evalNodeSet(e.left, c)
	if err != nil {
		return nil, err
	}
	r, err := evalNodeSet(e.right, c)
	if err != nil {
		return nil, err
	}
	return sortUnique(append(slices.Clone(l), r...)), nil
}

func evalNodeSet(e expr, c *evalCtx) (NodeSet, error) {
	v, err := e.eval(c)
	if err != nil {
		return nil, err
	}
	ns, ok := v.(NodeSet)
	if !ok {
		return nil, fmt.Errorf("expected a node-set, got %s", typeName(v))
	}
	return ns, nil
}

type binaryExpr struct {
	op          string
	left, right expr
}

func (e *binaryExpr) eval(c *evalCtx) (Value, error) {
	l, err := e.left.eval(c)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "or":
		if toBool(l) {
			return true, nil
		}
		r, err := e.right.eval(c)
		return err == nil && toBool(r), err
	case "and":
		if !toBool(l) {
			return false, nil
		}
		r, err := e.right.eval(c)
		return err == nil && toBool(r), err
	}
	r, err := e.right.eval(c)
	if err != nil {
		return nil, err
	}
	switch e.op {
	case "=", "!=", "<", "<=", ">", ">=":
		return compare(e.op, l, r), nil
	}
	a, b := toNumber(l), toNumber(r)
	switch e.op {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "div":
		return a / b, nil
	case "mod":
		return math.Mod(a, b), nil
	}
	return nil, fmt.Errorf("unknown operator %s", e.op)
}

// compare implements XPath 1.0 section 3.4.
func compare(op string, l, r Value) bool {
	ln, lIsSet := l.(NodeSet)
	rn, rIsSet := r.(NodeSet)
	switch {
	case lIsSet && rIsSet:
		for _, a := range ln {
			av := a.StringValue()
			for _, b := range rn {
				if compareAtoms(op, av, b.StringValue()) {
					return true
				}
			}
		}
		return false
	case lIsSet:
		if b, ok := r.(bool); ok {
			return compareAtoms(op, len(ln) > 0, b)
		}
		for _, a := range ln {
			if compareAtoms(op, atomFor(a.StringValue(), r), r) {
				return true
			}
		}
		return false
	case rIsSet:
		if b, ok := l.(bool); ok {
			return compareAtoms(op, b, len(rn) > 0)
		}
		for _, b := range rn {
			if compareAtoms(op, l, atomFor(b.StringValue(), l)) {
				return true
			}
		}
		return false
	}
	return compareAtoms(op, l, r)
}

// atomFor converts a node's string-value to the type of the other operand
// when that is a number; strings stay strings.
func atomFor(s string, other Value) Value {
	if _, ok := other.(float64); ok {
		return stringToNumber(s)
	}
	return s
}

// compareAtoms compares two non-node-set values.
func compareAtoms(op string, l, r Value) bool {
	if op == "=" || op == "!=" {
		var eq bool
		_, lb := l.(bool)
		_, rb := r.(bool)
		_, lf := l.(float64)
		_, rf := r.(float64)
		switch {
		case lb || rb:
			eq = toBool(l) == toBool(r)
		case lf || rf:
			eq = toNumber(l) == toNumber(r)
		default:
			eq = toString(l) == toString(r)
		}
		return eq == (op == "=")
	}
	a, b := toNumber(l), toNumber(r)
	switch op {
	case "<":
		return a < b
	case "<=":
		return a <= b
	case ">":
		return a > b
	case ">=":
		return a >= b
	}
	return false
}

type filterExpr struct {
	primary expr
	preds   []expr
}

func (e *filterExpr) eval(c *evalCtx) (Value, error) {
	ns, err := evalNodeSet(e.primary, c)
	if err != nil {
		return nil, err
	}
	// Filter predicates use document order (XPath 1.0 section 3.3).
	return applyPredicates(ns, e.preds, c)
}

type pathExpr struct {
	absolute bool
	filter   expr
	steps    []step
}

func (e *pathExpr) eval(c *evalCtx) (Value, error) {
	var nodes NodeSet
	switch {
	case e.filter != nil:
		var err error
		if nodes, err = evalNodeSet(e.filter, c); err != nil {
			return nil, err
		}
	case e.absolute:
		root := c.node
		for root.Parent != nil {
			root = root.Parent
		}
		nodes = NodeSet{root}
	default:
		nodes = NodeSet{c.node}
	}
	for _, s := range e.steps {
		var out NodeSet
		for _, n := range nodes {
			selected, err := s.apply(n, c)
			if err != nil {
				return nil, err
			}
			out = append(out, selected...)
		}
		if len(nodes) > 1 {
			out = sortUnique(out)
		}
		nodes = out
	}
	return nodes, nil
}

type axis uint8

const (
	axisChild axis = iota
	axisDescendant
	axisParent
	axisAncestor
	axisFollowingSibling
	axisPrecedingSibling
	axisFollowing
	axisPreceding
	axisAttribute
	axisNamespace
	axisSelf
	axisDescendantOrSelf
	axisAncestorOrSelf
)

var axisNames = map[string]axis{
	"child": axisChild, "descendant": axisDescendant, "parent": axisParent, "ancestor": axisAncestor,
	"following-sibling": axisFollowingSibling, "preceding-sibling": axisPrecedingSibling,
	"following": axisFollowing, "preceding": axisPreceding, "attribute": axisAttribute,
	"namespace": axisNamespace, "self": axisSelf, "descendant-or-self": axisDescendantOrSelf,
	"ancestor-or-self": axisAncestorOrSelf,
}

func (a axis) reverse() bool {
	return a == axisParent || a == axisAncestor || a == axisAncestorOrSelf ||
		a == axisPreceding || a == axisPrecedingSibling
}

func principalKind(a axis) NodeKind {
	switch a {
	case axisAttribute:
		return AttributeNode
	case axisNamespace:
		return NamespaceNode
	}
	return ElementNode
}

type testKind uint8

const (
	testName testKind = iota
	testNode
	testText
	testComment
	testPI
)

type nodeTest struct {
	kind      testKind
	principal NodeKind
	anyLocal  bool
	local     string
	space     string
	hasSpace  bool // the test names a namespace: prefix:* or prefix:local
	hasLocal  bool // processing-instruction('target')
}

func (t nodeTest) match(n *Node) bool {
	switch t.kind {
	case testNode:
		return true
	case testText:
		return n.Kind == TextNode
	case testComment:
		return n.Kind == CommentNode
	case testPI:
		return n.Kind == ProcessingInstructionNode && (!t.hasLocal || n.Local == t.local)
	}
	if n.Kind != t.principal {
		return false
	}
	if t.principal == NamespaceNode {
		// A namespace node has a null namespace URI and the prefix as name.
		return !t.hasSpace && (t.anyLocal || n.Local == t.local)
	}
	if t.anyLocal {
		return !t.hasSpace || n.Space == t.space
	}
	return n.Local == t.local && n.Space == t.space
}

type step struct {
	axis  axis
	test  nodeTest
	preds []expr
}

// apply returns the nodes the step selects from n, in document order.
func (s step) apply(n *Node, c *evalCtx) (NodeSet, error) {
	var nodes NodeSet
	s.collect(n, func(m *Node) {
		if s.test.match(m) {
			nodes = append(nodes, m)
		}
	})
	// nodes is in axis order, which predicates use for proximity positions.
	nodes, err := applyPredicates(nodes, s.preds, c)
	if err != nil {
		return nil, err
	}
	if s.axis.reverse() {
		slices.Reverse(nodes)
	}
	return nodes, nil
}

// collect visits the nodes on the axis from n, in axis order.
func (s step) collect(n *Node, visit func(*Node)) {
	switch s.axis {
	case axisSelf:
		visit(n)
	case axisChild:
		for _, c := range n.Children {
			visit(c)
		}
	case axisAttribute:
		for _, a := range n.Attrs {
			visit(a)
		}
	case axisNamespace:
		for _, ns := range n.namespaces() {
			visit(ns)
		}
	case axisParent:
		if n.Parent != nil {
			visit(n.Parent)
		}
	case axisAncestorOrSelf:
		visit(n)
		fallthrough
	case axisAncestor:
		for p := n.Parent; p != nil; p = p.Parent {
			visit(p)
		}
	case axisDescendantOrSelf:
		visit(n)
		fallthrough
	case axisDescendant:
		descendants(n, visit)
	case axisFollowingSibling:
		if n.Parent != nil && isChildKind(n) {
			for _, c := range n.Parent.Children[n.index+1:] {
				visit(c)
			}
		}
	case axisPrecedingSibling:
		if n.Parent != nil && isChildKind(n) {
			for i := n.index - 1; i >= 0; i-- {
				visit(n.Parent.Children[i])
			}
		}
	case axisFollowing:
		// Everything after n in document order, except its descendants.
		start := n
		if !isChildKind(n) {
			start = n.Parent
			descendants(start, visit) // attributes/namespaces: the element's content follows
		}
		for m := start; m.Parent != nil; m = m.Parent {
			for _, sib := range m.Parent.Children[m.index+1:] {
				visit(sib)
				descendants(sib, visit)
			}
		}
	case axisPreceding:
		// Everything before n in document order, except its ancestors,
		// in reverse document order.
		start := n
		if !isChildKind(n) {
			start = n.Parent
		}
		for m := start; m.Parent != nil; m = m.Parent {
			for i := m.index - 1; i >= 0; i-- {
				sib := m.Parent.Children[i]
				reverseDescendants(sib, visit)
				visit(sib)
			}
		}
	}
}

func isChildKind(n *Node) bool {
	return n.Kind != AttributeNode && n.Kind != NamespaceNode && n.Kind != RootNode
}

func descendants(n *Node, visit func(*Node)) {
	for _, c := range n.Children {
		visit(c)
		descendants(c, visit)
	}
}

func reverseDescendants(n *Node, visit func(*Node)) {
	for i := len(n.Children) - 1; i >= 0; i-- {
		reverseDescendants(n.Children[i], visit)
		visit(n.Children[i])
	}
}

// applyPredicates filters nodes, whose order gives the proximity positions.
func applyPredicates(nodes NodeSet, preds []expr, c *evalCtx) (NodeSet, error) {
	for _, pred := range preds {
		var kept NodeSet
		for i, n := range nodes {
			v, err := pred.eval(c.with(n, i+1, len(nodes)))
			if err != nil {
				return nil, err
			}
			if num, ok := v.(float64); ok {
				if num == float64(i+1) {
					kept = append(kept, n)
				}
			} else if toBool(v) {
				kept = append(kept, n)
			}
		}
		nodes = kept
	}
	return nodes, nil
}

func sortUnique(ns NodeSet) NodeSet {
	slices.SortFunc(ns, func(a, b *Node) int {
		switch {
		case a == b:
			return 0
		case before(a, b):
			return -1
		}
		return 1
	})
	return slices.Compact(ns)
}

func typeName(v Value) string {
	switch v.(type) {
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	}
	return "node-set"
}

func toBool(v Value) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	case NodeSet:
		return len(x) > 0
	}
	return false
}

func toNumber(v Value) float64 {
	switch x := v.(type) {
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return stringToNumber(x)
	case NodeSet:
		return stringToNumber(toString(x))
	}
	return math.NaN()
}

// stringToNumber follows XPath 1.0 section 4.4: optional whitespace, an
// optional minus sign, digits with an optional decimal point, optional
// whitespace. Anything else, including an exponent or "+", is NaN.
func stringToNumber(s string) float64 {
	s = strings.Trim(s, " \t\n\r")
	body := strings.TrimPrefix(s, "-")
	if body == "" || body == "." {
		return math.NaN()
	}
	dot := false
	for i := 0; i < len(body); i++ {
		switch c := body[i]; {
		case c == '.' && !dot:
			dot = true
		case c >= '0' && c <= '9':
		default:
			return math.NaN()
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

func toString(v Value) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return numberToString(x)
	case string:
		return x
	case NodeSet:
		if len(x) == 0 {
			return ""
		}
		return x[0].StringValue()
	}
	return ""
}

// numberToString follows XPath 1.0 section 4.2: no exponent, no trailing
// zeros, integers without a decimal point.
func numberToString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
