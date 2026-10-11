package xpath

import (
	"errors"
	"strings"
)

// CountByAttribute is the name of an extension function that counts
// elements by the value of an attribute, using an index built once per
// document:
//
//	qtiv:count-by-attribute(scope, axis, names, attribute, value)
//
// returns the same number as
//
//	count(scope/axis::*[self::n1 or self::n2 or ...][@attribute = value])
//
// where axis is 'child' or 'descendant', names lists the element names
// n1, n2, ... in Clark notation ({namespace-uri}local-name), separated by
// spaces, attribute is the local name of an attribute without namespace,
// and value is a node-set that does not depend on the context node, such
// as current()/@identifier. Counting this way is linear in the size of the
// scope once, where the XPath expression is linear for every call: rules
// that count, for each of n elements, the others with the same identifier
// would take time n². The schematron package substitutes it for such
// expressions. The prefix needs no namespace binding.
const CountByAttribute = "qtiv:count-by-attribute"

// attrIndexKey identifies an index: the counts per attribute value of the
// elements with the given names on the axis from the scope node.
type attrIndexKey struct {
	scope             *Node
	axis, names, attr string
}

func countByAttribute(_ *evalCtx, a []Value) (Value, error) {
	scope, err := nodeSetArg(a[0])
	if err != nil {
		return nil, err
	}
	axis, names, attr := toString(a[1]), toString(a[2]), toString(a[3])
	if axis != "child" && axis != "descendant" {
		return nil, errors.New("axis must be 'child' or 'descendant'")
	}
	values, err := nodeSetArg(a[4])
	if err != nil {
		return nil, err
	}
	if len(scope) == 0 || len(values) == 0 {
		return 0.0, nil
	}
	var counts map[string]int
	if len(scope) == 1 {
		counts = cachedAttributeCounts(attrIndexKey{scope: scope[0], axis: axis, names: names, attr: attr})
	} else {
		counts = attributeCounts(scope, axis, parseClarkNames(names), attr)
	}
	// Each element has one value, so the counts of distinct values add up.
	total := 0
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		s := v.StringValue()
		if !seen[s] {
			seen[s] = true
			total += counts[s]
		}
	}
	return float64(total), nil
}

// cachedAttributeCounts returns the counts for key, built on first use and
// kept on the root of the document.
func cachedAttributeCounts(key attrIndexKey) map[string]int {
	root := key.scope
	for root.Parent != nil {
		root = root.Parent
	}
	if counts, ok := root.attrIndexes[key]; ok {
		return counts
	}
	counts := attributeCounts(NodeSet{key.scope}, key.axis, parseClarkNames(key.names), key.attr)
	if root.attrIndexes == nil {
		root.attrIndexes = map[attrIndexKey]map[string]int{}
	}
	root.attrIndexes[key] = counts
	return counts
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

// attributeCounts counts the elements with the given names on the axis from
// the scope nodes by the value of their attribute. Elements without the
// attribute are not counted. An element is counted once, even when the
// subtrees of the scope nodes overlap.
func attributeCounts(scope NodeSet, axis string, names map[clarkName]bool, attr string) map[string]int {
	k := attrCounter{names: names, attr: attr, counts: map[string]int{}}
	if axis == "child" {
		for _, s := range scope {
			for _, ch := range s.Children {
				k.add(ch)
			}
		}
		return k.counts
	}
	in := make(map[*Node]bool, len(scope))
	for _, s := range scope {
		in[s] = true
	}
	for _, s := range scope {
		if !hasAncestorIn(s, in) { // otherwise its subtree is counted already
			k.addDescendants(s)
		}
	}
	return k.counts
}

func hasAncestorIn(n *Node, set map[*Node]bool) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		if set[p] {
			return true
		}
	}
	return false
}

type attrCounter struct {
	names  map[clarkName]bool
	attr   string
	counts map[string]int
}

func (k *attrCounter) add(n *Node) {
	if n.Kind != ElementNode || !k.names[clarkName{n.Space, n.Local}] {
		return
	}
	for _, at := range n.Attrs {
		if at.Space == "" && at.Local == k.attr {
			k.counts[at.Data]++
			return
		}
	}
}

func (k *attrCounter) addDescendants(n *Node) {
	for _, ch := range n.Children {
		k.add(ch)
		k.addDescendants(ch)
	}
}
