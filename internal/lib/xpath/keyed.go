package xpath

import (
	"math/bits"
	"slices"
)

// keyedPath is an absolute path whose last predicate compares an attribute
// with a value that does not depend on the predicate's context, such as
//
//	//a[@id = current()/@ref]
//
// A rule that evaluates it at each of n elements walks the document n
// times. Without its last predicate the path is independent, so a session
// evaluates that prefix once per document and indexes the nodes it selects
// by their attribute; each evaluation then evaluates the value once and
// looks it up.
//
// The predicate keeps a node when the string-value of its attribute equals
// the value, or the string-value of a node of it, which depends only on the
// node: filtering the nodes of the prefix gives the nodes of the path. A
// value that turns out to be a number or a boolean compares differently,
// and the path is then evaluated as written.
type keyedPath struct {
	prefix *pathExpr
	attr   nodeTest
	value  expr
}

// keyIndex holds the nodes of a prefix by the value of their attribute,
// each list in document order.
type keyIndex struct {
	byValue map[string]NodeSet
	// empty: the prefix selects no node.
	empty bool
}

// markKeyed sets p.keyed when p has the form of a keyedPath.
func markKeyed(p *pathExpr) {
	if !p.absolute || p.filter != nil || p.independent || len(p.steps) == 0 {
		return
	}
	last := p.steps[len(p.steps)-1]
	if len(last.preds) == 0 {
		return
	}
	attr, value, ok := attributeEquals(last.preds[len(last.preds)-1])
	if !ok || !contextFree(value) {
		return
	}
	steps := slices.Clone(p.steps)
	trimmed := last
	trimmed.preds = last.preds[: len(last.preds)-1 : len(last.preds)-1]
	trimmed.first = selectsFirst(trimmed.preds)
	trimmed.byNode = !slices.ContainsFunc(trimmed.preds, func(p expr) bool { return !filtersByNode(p) })
	steps[len(steps)-1] = trimmed
	prefix := &pathExpr{absolute: true, steps: steps}
	if dependsOnContext(prefix) {
		return
	}
	prefix.independent = true
	p.keyed = &keyedPath{prefix: prefix, attr: attr, value: value}
}

// attributeEquals matches @name = value and value = @name, where @name is
// an attribute step without predicates that names one attribute.
func attributeEquals(e expr) (nodeTest, expr, bool) {
	b, ok := e.(*binaryExpr)
	if !ok || b.op != "=" {
		return nodeTest{}, nil, false
	}
	if t, ok := attributeStep(b.left); ok {
		return t, b.right, true
	}
	if t, ok := attributeStep(b.right); ok {
		return t, b.left, true
	}
	return nodeTest{}, nil, false
}

func attributeStep(e expr) (nodeTest, bool) {
	p, ok := e.(*pathExpr)
	if !ok || p.absolute || p.filter != nil || len(p.steps) != 1 {
		return nodeTest{}, false
	}
	s := p.steps[0]
	if s.axis != axisAttribute || s.test.kind != testName || s.test.anyLocal || len(s.preds) != 0 {
		return nodeTest{}, false
	}
	return s.test, true
}

// contextFree reports whether e has the same value at every context node,
// position and size of a document: it refers to the context only through
// current(), variables and absolute paths. Predicates inside e have
// contexts of their own.
func contextFree(e expr) bool {
	switch x := e.(type) {
	case *callExpr:
		switch {
		case x.name == "lang":
			return false
		case len(x.args) == 0:
			// position(), last() and the functions whose argument
			// defaults to the context node.
			return x.name == "current" || x.name == "true" || x.name == "false"
		}
	case *pathExpr:
		if x.filter == nil {
			return x.absolute
		}
	}
	return !slices.ContainsFunc(sameContext(e), func(sub expr) bool { return !contextFree(sub) })
}

// lookup evaluates p, a keyed path, in the document of root.
func (k *keyedPath) lookup(p *pathExpr, root *Node, c *evalCtx) (NodeSet, error) {
	idx, err := c.s.keyIndex(p, root, func() (keyIndex, error) { return k.index(root, c) })
	if err != nil {
		return nil, err
	}
	if idx.empty {
		return nil, nil // as written, the predicate is never evaluated
	}
	v, err := k.value.eval(c)
	if err != nil {
		return nil, err
	}
	var keys []string
	switch x := v.(type) {
	case string:
		keys = []string{x}
	case NodeSet:
		if err := c.s.charge(len(x)); err != nil {
			return nil, err
		}
		keys = make([]string, len(x))
		for i, n := range x {
			if keys[i], err = c.s.stringValue(n); err != nil {
				return nil, err
			}
		}
	default:
		return p.walk(NodeSet{root}, c)
	}
	var lists []NodeSet
	var seen map[string]bool // a value may repeat
	if len(keys) > 1 {
		seen = make(map[string]bool, len(keys))
	}
	size := 0
	for _, key := range keys {
		if nodes := idx.byValue[key]; len(nodes) > 0 && !seen[key] {
			if seen != nil {
				seen[key] = true
			}
			lists = append(lists, nodes)
			size += len(nodes)
		}
	}
	if err := c.s.charge(1 + size); err != nil {
		return nil, err
	}
	switch len(lists) {
	case 0:
		return nil, nil
	case 1:
		return lists[0], nil // shared: its capacity is clipped
	}
	// Sorting the lists into one compares each node about log2(size) times,
	// which the step per node above does not cover.
	if err := c.s.charge(size * bits.Len(uint(size))); err != nil {
		return nil, err
	}
	out := make(NodeSet, 0, size)
	for _, l := range lists {
		out = append(out, l...)
	}
	return sortUnique(out), nil
}

// index evaluates the prefix and indexes its nodes by their attribute. It
// charges a step for each node and for each attribute it reads, as the
// attribute step of the predicate would.
func (k *keyedPath) index(root *Node, c *evalCtx) (keyIndex, error) {
	nodes, err := c.s.remember(k.prefix, root, func() (NodeSet, error) { return k.prefix.walk(NodeSet{root}, c) })
	if err != nil {
		return keyIndex{}, err
	}
	idx := keyIndex{byValue: map[string]NodeSet{}, empty: len(nodes) == 0}
	for _, n := range nodes {
		if err := c.s.charge(1 + len(n.Attrs)); err != nil {
			return keyIndex{}, err
		}
		for _, a := range n.Attrs {
			if k.attr.match(a) {
				idx.byValue[a.Data] = append(idx.byValue[a.Data], n)
				break
			}
		}
	}
	for v, l := range idx.byValue {
		idx.byValue[v] = l[:len(l):len(l)]
	}
	return idx, nil
}
