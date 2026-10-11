package xpath

import (
	"errors"
	"slices"
	"sort"
	"strings"
)

// ExtensionNamespace is the namespace of the extension functions. Only a
// prefix bound to it calls them, so an expression from elsewhere cannot
// call one by accident, whatever prefixes it uses.
const ExtensionNamespace = "urn:x-qti3-validator:xpath-extensions"

// CountByAttribute is the local name of an extension function that counts
// elements by the value of an attribute, using an index of the document:
//
//	ext:count-by-attribute(scope, axis, names, attribute, value)
//
// with ext bound to ExtensionNamespace, returns the same number as
//
//	count(scope/axis::*[self::n1 or self::n2 or ...][@attribute = value])
//
// where axis is 'child' or 'descendant', names lists the element names
// n1, n2, ... in Clark notation ({namespace-uri}local-name), separated by
// spaces, attribute is the local name of an attribute without namespace,
// and value is a node-set.
//
// The XPath expression walks the subtrees of the scope on every call, so
// rules that count, for each of n elements, the others with the same
// identifier take time n². The function instead indexes the whole document
// once per axis, names and attribute, in a step per node, keeping a few
// words per matching element. A call then costs a step per scope node and
// per value, plus binary searches, however large the subtrees of the scope
// are and however deeply scopes nest. The schematron package substitutes
// it for such expressions.
const CountByAttribute = "count-by-attribute"

// attrIndexKey identifies an index of a document: the elements with the
// given names on the axis, by the value of their attribute.
type attrIndexKey struct {
	axis, names, attr string
}

// attrIndex is an index of a document for one attrIndexKey: per attribute
// value, a position for each matching element, ascending. On the
// descendant axis the position is the element's document order, and the
// descendants of a node are the elements whose order lies in a span of
// the node. On the child axis it is the order of the element's parent, and
// the span of a node holds only its own order.
type attrIndex struct {
	positions map[string][]int
}

func countByAttribute(c *evalCtx, a []Value) (Value, error) {
	scope, err := nodeSetArg(a[0])
	if err != nil {
		return nil, err
	}
	key := attrIndexKey{axis: toString(a[1]), names: toString(a[2]), attr: toString(a[3])}
	if key.axis != "child" && key.axis != "descendant" {
		return nil, errors.New("axis must be 'child' or 'descendant'")
	}
	values, err := nodeSetArg(a[4])
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 || len(values) == 0 {
		return 0.0, nil
	}
	idx, err := documentIndex(c.s, scope[0], key)
	if err != nil {
		return nil, err
	}
	if err := c.s.charge(len(scope) + len(values)); err != nil {
		return nil, err
	}
	// Each element has one value, so the counts of distinct values add up.
	distinct := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if s := v.StringValue(); !seen[s] {
			seen[s] = true
			distinct = append(distinct, s)
		}
	}
	var spans []span
	if key.axis == "child" {
		spans = ownSpans(scope)
	} else {
		spans = descendantSpans(scope)
	}
	total := 0
	for _, v := range distinct {
		n, work := countInSpans(idx.positions[v], spans)
		if err := c.s.charge(work); err != nil {
			return nil, err
		}
		total += n
	}
	return float64(total), nil
}

// documentIndex returns the index for key of the document of n, built in
// session s on first use and kept on the root of the document.
func documentIndex(s *Session, n *Node, key attrIndexKey) (*attrIndex, error) {
	root, hops := n, 0
	for root.Parent != nil {
		root = root.Parent
		hops++
	}
	if err := s.charge(hops); err != nil {
		return nil, err
	}
	if idx, ok := root.attrIndexes[key]; ok {
		return idx, nil
	}
	idx, err := buildIndex(s, root, key)
	if err != nil {
		return nil, err // nothing is kept, the session is stopped
	}
	if root.attrIndexes == nil {
		root.attrIndexes = map[attrIndexKey]*attrIndex{}
	}
	root.attrIndexes[key] = idx
	return idx, nil
}

// buildIndex visits every node of the document of root once, in document
// order, and charges a step for each node and each attribute it reads.
// Elements without the attribute are not indexed.
func buildIndex(s *Session, root *Node, key attrIndexKey) (*attrIndex, error) {
	names := parseClarkNames(key.names)
	idx := &attrIndex{positions: map[string][]int{}}
	stack := []*Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if err := s.charge(1); err != nil {
			return nil, err
		}
		for i := len(n.Children) - 1; i >= 0; i-- {
			stack = append(stack, n.Children[i])
		}
		if n.Kind != ElementNode || !names[clarkName{n.Space, n.Local}] {
			continue
		}
		if err := s.charge(len(n.Attrs)); err != nil {
			return nil, err
		}
		if value, ok := plainAttribute(n, key.attr); ok {
			pos := n.order
			if key.axis == "child" {
				pos = n.Parent.order
			}
			idx.positions[value] = append(idx.positions[value], pos)
		}
	}
	if key.axis == "child" {
		// Children come in document order, their parents not always.
		for _, ps := range idx.positions {
			if err := s.charge(len(ps)); err != nil {
				return nil, err
			}
			slices.Sort(ps)
		}
	}
	return idx, nil
}

// plainAttribute returns the value of the attribute of n with the given
// local name and no namespace.
func plainAttribute(n *Node, local string) (string, bool) {
	for _, at := range n.Attrs {
		if at.Space == "" && at.Local == local {
			return at.Data, true
		}
	}
	return "", false
}

type clarkName struct{ space, local string }

// parseClarkNames reads a space-separated list of {namespace-uri}local names;
// a name without braces has no namespace.
func parseClarkNames(s string) map[clarkName]bool {
	names := map[clarkName]bool{}
	for _, f := range strings.Fields(s) {
		var n clarkName
		if rest, ok := strings.CutPrefix(f, "{"); ok {
			n.space, n.local, _ = strings.Cut(rest, "}")
		} else {
			n.local = f
		}
		names[n] = true
	}
	return names
}

// span is a range [lo, hi) of document order.
type span struct{ lo, hi int }

// ownSpans returns the spans of the orders of the nodes that can have
// children, ascending; they are apart, as each node has an order of its own.
func ownSpans(nodes NodeSet) []span {
	spans := make([]span, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind == RootNode || n.Kind == ElementNode {
			spans = append(spans, span{n.order, n.order + 1})
		}
	}
	sortSpans(spans)
	return spans
}

// sortSpans sorts spans by their start. A node-set is in document order
// already, so this only checks, in case one is not.
func sortSpans(spans []span) {
	byStart := func(a, b span) int { return a.lo - b.lo }
	if !slices.IsSortedFunc(spans, byStart) {
		slices.SortFunc(spans, byStart)
	}
}

// descendantSpans returns the spans of the descendants of the nodes,
// ascending and without overlap: a node inside the subtree of another adds
// nothing. Only roots and elements have descendants.
func descendantSpans(nodes NodeSet) []span {
	spans := make([]span, 0, len(nodes))
	for _, n := range nodes {
		if (n.Kind == RootNode || n.Kind == ElementNode) && n.end > n.order+1 {
			spans = append(spans, span{n.order + 1, n.end})
		}
	}
	sortSpans(spans)
	// Subtrees nest or are apart, so a span either lies within the last one
	// kept or after it.
	merged := spans[:0]
	for _, sp := range spans {
		if last := len(merged) - 1; last >= 0 && sp.lo < merged[last].hi {
			merged[last].hi = max(merged[last].hi, sp.hi)
			continue
		}
		merged = append(merged, sp)
	}
	return merged
}

// countInSpans counts the positions, ascending, that lie in one of the
// spans, which are ascending and apart. It searches the shorter list in
// the longer and also returns that length, the number of searches.
func countInSpans(positions []int, spans []span) (count, searches int) {
	if len(positions) <= len(spans) {
		for _, p := range positions {
			// The first span that ends after p contains p if it starts
			// at or before it.
			i := sort.Search(len(spans), func(i int) bool { return spans[i].hi > p })
			if i < len(spans) && spans[i].lo <= p {
				count++
			}
		}
		return count, len(positions)
	}
	for _, sp := range spans {
		count += sort.SearchInts(positions, sp.hi) - sort.SearchInts(positions, sp.lo)
	}
	return count, len(spans)
}
