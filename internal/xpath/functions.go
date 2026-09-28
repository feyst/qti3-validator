package xpath

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type function struct {
	minArgs, maxArgs int // maxArgs -1: unbounded
	call             func(c *evalCtx, args []Value) (Value, error)
}

type callExpr struct {
	name string
	fn   function
	args []expr
}

func (e *callExpr) eval(c *evalCtx) (Value, error) {
	args := make([]Value, len(e.args))
	for i, a := range e.args {
		v, err := a.eval(c)
		if err != nil {
			return nil, err
		}
		args[i] = v
	}
	v, err := e.fn.call(c, args)
	if err != nil {
		return nil, fmt.Errorf("%s(): %w", e.name, err)
	}
	return v, nil
}

// unsupportedFunctions are XSLT 1.0 functions that need more than a single
// document (document, key) or the XSLT processor itself.
var unsupportedFunctions = map[string]bool{
	"document": true, "key": true, "format-number": true, "system-property": true,
	"element-available": true, "function-available": true, "unparsed-entity-uri": true,
}

var functions map[string]function

func init() {
	functions = map[string]function{
		// Node-set functions, XPath 1.0 section 4.1.
		"last":     {0, 0, func(c *evalCtx, _ []Value) (Value, error) { return float64(c.size), nil }},
		"position": {0, 0, func(c *evalCtx, _ []Value) (Value, error) { return float64(c.pos), nil }},
		"count": {1, 1, func(_ *evalCtx, a []Value) (Value, error) {
			ns, err := nodeSetArg(a[0])
			return float64(len(ns)), err
		}},
		// Without a DTD no attribute has type ID, so id() selects nothing.
		"id": {1, 1, func(*evalCtx, []Value) (Value, error) { return NodeSet{}, nil }},
		"local-name": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			n, err := optionalNode(c, a)
			if n == nil || err != nil {
				return "", err
			}
			if n.Kind == NamespaceNode || n.Kind == ProcessingInstructionNode {
				return n.Local, nil
			}
			if n.Kind == ElementNode || n.Kind == AttributeNode {
				return n.Local, nil
			}
			return "", nil
		}},
		"namespace-uri": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			n, err := optionalNode(c, a)
			if n == nil || err != nil {
				return "", err
			}
			if n.Kind == ElementNode || n.Kind == AttributeNode {
				return n.Space, nil
			}
			return "", nil
		}},
		"name": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			n, err := optionalNode(c, a)
			if n == nil || err != nil {
				return "", err
			}
			return n.Name(), nil
		}},

		// String functions, section 4.2.
		"string": {0, 1, func(c *evalCtx, a []Value) (Value, error) { return toString(contextArg(c, a)), nil }},
		"concat": {2, -1, func(_ *evalCtx, a []Value) (Value, error) {
			var b strings.Builder
			for _, v := range a {
				b.WriteString(toString(v))
			}
			return b.String(), nil
		}},
		"starts-with": {2, 2, func(_ *evalCtx, a []Value) (Value, error) {
			return strings.HasPrefix(toString(a[0]), toString(a[1])), nil
		}},
		"contains": {2, 2, func(_ *evalCtx, a []Value) (Value, error) {
			return strings.Contains(toString(a[0]), toString(a[1])), nil
		}},
		"substring-before": {2, 2, func(_ *evalCtx, a []Value) (Value, error) {
			before, _, ok := strings.Cut(toString(a[0]), toString(a[1]))
			if !ok {
				return "", nil
			}
			return before, nil
		}},
		"substring-after": {2, 2, func(_ *evalCtx, a []Value) (Value, error) {
			_, after, _ := strings.Cut(toString(a[0]), toString(a[1]))
			return after, nil
		}},
		"substring": {2, 3, func(_ *evalCtx, a []Value) (Value, error) {
			s := []rune(toString(a[0]))
			start := round(toNumber(a[1]))
			end := math.Inf(1)
			if len(a) == 3 {
				end = start + round(toNumber(a[2]))
			}
			var b strings.Builder
			for i, r := range s {
				if p := float64(i + 1); p >= start && p < end {
					b.WriteRune(r)
				}
			}
			return b.String(), nil
		}},
		"string-length": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			return float64(len([]rune(toString(contextArg(c, a))))), nil
		}},
		"normalize-space": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			return strings.Join(strings.FieldsFunc(toString(contextArg(c, a)), func(r rune) bool {
				return r == ' ' || r == '\t' || r == '\n' || r == '\r'
			}), " "), nil
		}},
		"translate": {3, 3, func(_ *evalCtx, a []Value) (Value, error) {
			from, to := []rune(toString(a[1])), []rune(toString(a[2]))
			mapping := map[rune]rune{}
			for i, r := range from {
				if _, seen := mapping[r]; seen {
					continue // the first occurrence wins
				}
				if i < len(to) {
					mapping[r] = to[i]
				} else {
					mapping[r] = -1 // removed
				}
			}
			var b strings.Builder
			for _, r := range toString(a[0]) {
				if m, ok := mapping[r]; ok {
					if m >= 0 {
						b.WriteRune(m)
					}
					continue
				}
				b.WriteRune(r)
			}
			return b.String(), nil
		}},

		// Boolean functions, section 4.3.
		"boolean": {1, 1, func(_ *evalCtx, a []Value) (Value, error) { return toBool(a[0]), nil }},
		"not":     {1, 1, func(_ *evalCtx, a []Value) (Value, error) { return !toBool(a[0]), nil }},
		"true":    {0, 0, func(*evalCtx, []Value) (Value, error) { return true, nil }},
		"false":   {0, 0, func(*evalCtx, []Value) (Value, error) { return false, nil }},
		"lang": {1, 1, func(c *evalCtx, a []Value) (Value, error) {
			want := strings.ToLower(toString(a[0]))
			for n := c.node; n != nil; n = n.Parent {
				for _, at := range n.Attrs {
					if at.Space == NamespaceXML && at.Local == "lang" {
						got := strings.ToLower(at.Data)
						return got == want || strings.HasPrefix(got, want+"-"), nil
					}
				}
			}
			return false, nil
		}},

		// Number functions, section 4.4.
		"number": {0, 1, func(c *evalCtx, a []Value) (Value, error) { return toNumber(contextArg(c, a)), nil }},
		"sum": {1, 1, func(_ *evalCtx, a []Value) (Value, error) {
			ns, err := nodeSetArg(a[0])
			sum := 0.0
			for _, n := range ns {
				sum += stringToNumber(n.StringValue())
			}
			return sum, err
		}},
		"floor":   {1, 1, func(_ *evalCtx, a []Value) (Value, error) { return math.Floor(toNumber(a[0])), nil }},
		"ceiling": {1, 1, func(_ *evalCtx, a []Value) (Value, error) { return math.Ceil(toNumber(a[0])), nil }},
		"round":   {1, 1, func(_ *evalCtx, a []Value) (Value, error) { return round(toNumber(a[0])), nil }},

		// XSLT 1.0 functions available to Schematron's default query binding.
		"current": {0, 0, func(c *evalCtx, _ []Value) (Value, error) { return NodeSet{c.current}, nil }},
		"generate-id": {0, 1, func(c *evalCtx, a []Value) (Value, error) {
			n, err := optionalNode(c, a)
			if n == nil || err != nil {
				return "", err
			}
			id := "n" + strconv.Itoa(n.order)
			if n.Kind == NamespaceNode {
				id += "-" + strconv.Itoa(n.nsOrder)
			}
			return id, nil
		}},
	}
}

// round implements XPath round(): halves go up, -0.5 <= x < 0 gives -0.
func round(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return f
	}
	if f < 0 && f >= -0.5 {
		return math.Copysign(0, -1)
	}
	return math.Floor(f + 0.5)
}

func nodeSetArg(v Value) (NodeSet, error) {
	ns, ok := v.(NodeSet)
	if !ok {
		return nil, fmt.Errorf("argument is a %s, not a node-set", typeName(v))
	}
	return ns, nil
}

// optionalNode returns the first node of the argument, or the context node
// when there is no argument. It returns nil for an empty node-set.
func optionalNode(c *evalCtx, a []Value) (*Node, error) {
	if len(a) == 0 {
		return c.node, nil
	}
	ns, err := nodeSetArg(a[0])
	if err != nil || len(ns) == 0 {
		return nil, err
	}
	return ns[0], nil
}

// contextArg returns the argument, or a node-set with the context node.
func contextArg(c *evalCtx, a []Value) Value {
	if len(a) == 0 {
		return NodeSet{c.node}
	}
	return a[0]
}
