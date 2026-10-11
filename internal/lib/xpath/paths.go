package xpath

import (
	"strconv"
	"strings"
)

// Paths gives the Path of many nodes of one document. Node.Path counts,
// for the node and each of its ancestors, the siblings of the same name
// before it; for every one of many siblings that is the square of their
// number. Paths remembers, per parent, how far it has counted, and the
// paths of the elements it was asked for: the paths of nodes asked for in
// document order take one pass over their siblings together. Nodes out of
// that order are counted as Node.Path does.
//
// The zero value is ready to use. It is not safe for concurrent use.
type Paths struct {
	counted map[*Node]*siblingCount
	paths   map[*Node]string // of the elements asked for, and their ancestors
}

// siblingCount is how many element children of each name a parent has
// before the child at index next.
type siblingCount struct {
	next   int
	counts map[clarkName]int
}

// Path returns n.Path().
func (p *Paths) Path(n *Node) string {
	switch n.Kind {
	case RootNode:
		return "/"
	case ElementNode:
		return p.element(n)
	case AttributeNode:
		return strings.TrimSuffix(p.Path(n.Parent), "/") + "/@" + n.Name()
	}
	return p.Path(n.Parent)
}

func (p *Paths) element(n *Node) string {
	if path, ok := p.paths[n]; ok {
		return path
	}
	path := strings.TrimSuffix(p.Path(n.Parent), "/") + "/" + n.Name() + "[" + strconv.Itoa(p.position(n)) + "]"
	if p.paths == nil {
		p.paths = map[*Node]string{}
	}
	p.paths[n] = path
	return path
}

// position returns the 1-based position of the element n among the
// siblings of its name.
func (p *Paths) position(n *Node) int {
	parent, index := n.Parent, int(n.index)
	name := clarkName{n.Space, n.Local}
	sc := p.counted[parent]
	if sc == nil {
		if p.counted == nil {
			p.counted = map[*Node]*siblingCount{}
		}
		sc = &siblingCount{counts: map[clarkName]int{}}
		p.counted[parent] = sc
	}
	if index < sc.next {
		// Before where the count stands: count again up to n.
		pos := 1
		for _, s := range parent.Children[:index] {
			if s.Kind == ElementNode && s.Local == n.Local && s.Space == n.Space {
				pos++
			}
		}
		return pos
	}
	for _, s := range parent.Children[sc.next : index+1] {
		if s.Kind == ElementNode {
			sc.counts[clarkName{s.Space, s.Local}]++
		}
	}
	sc.next = index + 1
	return sc.counts[name]
}
