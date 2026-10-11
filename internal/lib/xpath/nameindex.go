package xpath

import "sort"

// nameIndex lists the elements of a document by expanded name, each list in
// document order.
type nameIndex map[clarkName]NodeSet

// minIndexedSubtree is the least number of nodes in the subtree of a node
// for which a descendant step by name uses the index of the document rather
// than walking the subtree, which is cheaper for a small one.
const minIndexedSubtree = 64

// fromIndex returns the nodes a descendant or descendant-or-self step with
// a name test selects from n before its predicates, in document order, from
// the index of the document: the elements of that name whose order lies in
// the subtree of n. It returns false when the step or n does not qualify;
// the nodes must then be collected from the axis.
//
// The index is built in a step per node the first time; a lookup costs a
// step, and one per node it returns. The node-set returned is
// shared, so its capacity is clipped.
func (s step) fromIndex(n *Node, c *evalCtx) (NodeSet, bool, error) {
	list, ok, err := s.indexRange(n, c)
	if !ok || err != nil {
		return nil, ok, err
	}
	if err := c.s.charge(1 + len(list)); err != nil {
		return nil, true, err
	}
	return list, true, nil
}

// indexRange is fromIndex without the charge for the lookup and the nodes:
// a caller that stops at one of them charges for those it looked at.
func (s step) indexRange(n *Node, c *evalCtx) (NodeSet, bool, error) {
	if !optimise || s.test.kind != testName || s.test.anyLocal ||
		(s.axis != axisDescendant && s.axis != axisDescendantOrSelf) ||
		(n.Kind != ElementNode && n.Kind != RootNode) || n.end-n.order < minIndexedSubtree {
		return nil, false, nil
	}
	idx, err := c.s.nameIndex(n.documentRoot())
	if err != nil {
		return nil, true, err
	}
	list := idx[clarkName{s.test.space, s.test.local}]
	first := n.order + 1 // the descendants
	if s.axis == axisDescendantOrSelf {
		first = n.order
	}
	lo := sort.Search(len(list), func(i int) bool { return list[i].order >= first })
	hi := lo + sort.Search(len(list)-lo, func(i int) bool { return list[lo+i].order >= n.end })
	return list[lo:hi:hi], true, nil
}

// nameIndex returns the index of the document of root, building it the
// first time at a step per node.
func (s *Session) nameIndex(root *Node) (nameIndex, error) {
	if idx, ok := s.names[root]; ok {
		return idx, nil
	}
	idx := nameIndex{}
	var walk func(*Node) error
	walk = func(n *Node) error {
		if err := s.charge(1); err != nil {
			return err
		}
		if n.Kind == ElementNode {
			k := clarkName{n.Space, n.Local}
			idx[k] = append(idx[k], n)
		}
		for _, ch := range n.Children {
			if err := walk(ch); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root); err != nil {
		return nil, err
	}
	if s.names == nil {
		s.names = map[*Node]nameIndex{}
	}
	s.names[root] = idx
	return idx, nil
}
