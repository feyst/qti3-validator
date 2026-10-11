// Package xpath implements XPath 1.0 (W3C Recommendation, 16 November 1999)
// over a small read-only DOM, plus the XSLT 1.0 functions current() and
// generate-id() that Schematron rules commonly use, and one extension
// function, count-by-attribute in ExtensionNamespace (see CountByAttribute).
//
// Compiled expressions are immutable and safe for concurrent use; a Document
// is not, and belongs to one goroutine.
package xpath

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// NamespaceXML is the namespace bound to the xml prefix.
const NamespaceXML = "http://www.w3.org/XML/1998/namespace"

// NodeKind is one of the seven XPath 1.0 node types.
type NodeKind uint8

// The node kinds.
const (
	RootNode NodeKind = iota
	ElementNode
	AttributeNode
	NamespaceNode
	TextNode
	CommentNode
	ProcessingInstructionNode
)

// Node is a node in the XPath data model.
//
// For elements and attributes, Prefix, Local and Space hold the name as
// written and its namespace URI. A namespace node has the prefix in Local and
// the URI in Data; a processing instruction has its target in Local.
type Node struct {
	Prefix string
	Local  string
	Space  string
	Data   string // attribute value, text, comment, PI data or namespace URI
	Parent *Node
	// Children of the root or an element, attributes of an element.
	Children []*Node
	Attrs    []*Node

	root *Node // the root of the document, set by Parse
	// extra holds what few nodes have, so the others do not pay for it; see
	// nodeExtra.
	extra *nodeExtra

	// Line and Column of the start of the node in the source (1-based).
	// Attributes report the position of their element.
	Line, Column int32

	// The positions are 32 bits wide, which halves them: Parse refuses a
	// document with more nodes than that holds.
	index int32 // position in the parent's Children or Attrs, or among the owner's namespace nodes
	order int32 // document order; attributes follow their element
	end   int32 // roots and elements: the order after their last descendant

	Kind NodeKind
}

// A Node is 160 bytes, and a document has one for every element, attribute
// and piece of text, so its size decides what a document costs in memory:
// internal/adapter/rules estimates that from it (see Measure). Keep it
// small: a field that only some nodes use belongs in nodeExtra.

// nodeExtra is the part of a Node that only some nodes have: the namespace
// declarations of an element, its namespace nodes once the namespace axis
// was used, and the indexes kept on the root.
type nodeExtra struct {
	nsDecls []nsBinding
	nsNodes []*Node // built on first use of the namespace axis
	// The root only: indexes of count-by-attribute, built on first use.
	attrIndexes map[attrIndexKey]*attrIndex
}

// more returns the extra part of n, allocating it the first time.
func (n *Node) more() *nodeExtra {
	if n.extra == nil {
		n.extra = &nodeExtra{}
	}
	return n.extra
}

// nsOrder is the 1-based position of a namespace node among those of its
// element, and 0 for any other node.
func (n *Node) nsOrder() int32 {
	if n.Kind == NamespaceNode {
		return n.index + 1
	}
	return 0
}

type nsBinding struct{ prefix, uri string }

// Document is a parsed XML document.
type Document struct {
	Root *Node
	// Nodes is the number of nodes below the root: elements, attributes,
	// text, comments and processing instructions. Namespace nodes, which
	// are built on demand, are not counted.
	Nodes int
}

// Name returns the qualified name as written, e.g. "xml:lang".
func (n *Node) Name() string {
	switch n.Kind {
	case ElementNode, AttributeNode:
		if n.Prefix != "" {
			return n.Prefix + ":" + n.Local
		}
		return n.Local
	case NamespaceNode, ProcessingInstructionNode:
		return n.Local
	}
	return ""
}

// StringValue returns the XPath string-value of the node.
func (n *Node) StringValue() string {
	v, _ := n.stringValue()
	return v
}

// stringValue returns the string-value of the node, and whether it was
// built for this call. The string-value of an attribute, a text node, a
// comment, a processing instruction or a namespace node is its Data, and
// that of an element whose only text is one text node, as in <a>text</a>
// or <a><b>text</b></a>, is the Data of that node: memory of the document.
// Only an element or a root with several text nodes below it has its text
// joined, which takes time and memory in proportion to that text on every
// call; a session charges for it (Session.stringValue).
func (n *Node) stringValue() (v string, built bool) {
	if n.Kind != RootNode && n.Kind != ElementNode {
		return n.Data, false
	}
	// Follow a chain of only children down to its text.
	only := n
	for len(only.Children) == 1 && only.Children[0].Kind == ElementNode {
		only = only.Children[0]
	}
	switch len(only.Children) {
	case 0:
		return "", false
	case 1:
		if c := only.Children[0]; c.Kind == TextNode {
			return c.Data, false
		}
		return "", false // a comment or a processing instruction
	}
	var b strings.Builder
	only.appendText(&b)
	return b.String(), true
}

func (n *Node) appendText(b *strings.Builder) {
	for _, c := range n.Children {
		switch c.Kind {
		case TextNode:
			b.WriteString(c.Data)
		case ElementNode:
			c.appendText(b)
		}
	}
}

// Path returns an XPath location of the node such as
// /qti-assessment-item[1]/qti-item-body[1]/@class, for error reports.
func (n *Node) Path() string {
	switch n.Kind {
	case RootNode:
		return "/"
	case AttributeNode:
		return strings.TrimSuffix(n.Parent.Path(), "/") + "/@" + n.Name()
	case ElementNode:
		pos := 1
		for _, s := range n.Parent.Children[:n.index] {
			if s.Kind == ElementNode && s.Local == n.Local && s.Space == n.Space {
				pos++
			}
		}
		return fmt.Sprintf("%s/%s[%d]", strings.TrimSuffix(n.Parent.Path(), "/"), n.Name(), pos)
	}
	return n.Parent.Path()
}

// documentRoot returns the root of the tree of n. Parse stores it on every
// node, so finding it does not depend on how deep the node lies; a node
// built otherwise is followed up to its root.
func (n *Node) documentRoot() *Node {
	if n.root != nil {
		return n.root
	}
	for n.Parent != nil {
		n = n.Parent
	}
	return n
}

// Before reports whether n precedes m in document order. Both must be nodes
// of one document.
func (n *Node) Before(m *Node) bool { return before(n, m) }

// before reports whether a precedes b in document order.
func before(a, b *Node) bool {
	if a.order != b.order {
		return a.order < b.order
	}
	return a.nsOrder() < b.nsOrder()
}

// namespaces returns the namespace nodes of an element: one per in-scope
// prefix, the xml prefix included.
func (n *Node) namespaces() []*Node {
	if n.Kind != ElementNode {
		return nil
	}
	if n.extra != nil && n.extra.nsNodes != nil {
		return n.extra.nsNodes
	}
	scope := map[string]string{}
	var prefixes []string
	for e := n; e != nil && e.Kind == ElementNode; e = e.Parent {
		if e.extra == nil {
			continue
		}
		for _, d := range e.extra.nsDecls {
			if _, seen := scope[d.prefix]; !seen {
				scope[d.prefix] = d.uri
				prefixes = append(prefixes, d.prefix)
			}
		}
	}
	if _, seen := scope["xml"]; !seen {
		scope["xml"] = NamespaceXML
		prefixes = append(prefixes, "xml")
	}
	nodes := make([]*Node, 0, len(prefixes))
	for _, p := range prefixes {
		if scope[p] == "" {
			continue // xmlns="" undeclares the default namespace
		}
		nodes = append(nodes, &Node{
			Kind: NamespaceNode, Local: p, Data: scope[p], Parent: n, root: n.root,
			order: n.order, Line: n.Line, Column: n.Column,
		})
	}
	for i, ns := range nodes {
		ns.index = int32(i)
	}
	n.more().nsNodes = nodes
	return nodes
}

// arena hands out the nodes of one document from chunks, so a node costs its
// own size and no more: allocated one by one, each would be rounded up to a
// size class and tracked on its own. The chunks live as long as any node of
// the document does, which is as long as the document.
type arena struct {
	free []Node
	next int // size of the next chunk
}

const maxArenaChunk = 1024

func (a *arena) node() *Node {
	if len(a.free) == 0 {
		a.next = min(max(2*a.next, 16), maxArenaChunk)
		a.free = make([]Node, a.next)
	}
	n := &a.free[0]
	a.free = a.free[1:]
	return n
}

// maxNodes is the most nodes a document may have: their positions are 32
// bits wide. A variable for the tests.
var maxNodes int32 = math.MaxInt32 - 1

// ErrTooManyNodes is returned by Parse for a document of more nodes than a
// Document can hold, some two thousand million.
var ErrTooManyNodes = errors.New("the document has too many nodes")

// position narrows a line or column of the source, which stops counting at
// the largest value a Node holds.
func position(n int) int32 {
	return int32(min(n, math.MaxInt32)) //nolint:gosec // bounded
}

// Parse is ParseContext without a context that stops it.
func Parse(r io.Reader) (*Document, error) {
	return ParseContext(context.Background(), r)
}

// parseCheckInterval is the number of tokens between checks of the context
// of ParseContext.
const parseCheckInterval = 1 << 10

// ParseContext reads a document into the XPath data model. It accepts no
// DTD: documents are expected to have passed the validator's checks, and a
// DOCTYPE is rejected here as well.
//
// The work is in proportion to the size of the document, however its text
// is cut into CDATA sections and however deep it nests. When ctx is done,
// it stops with the error of ctx.
func ParseContext(ctx context.Context, r io.Reader) (*Document, error) { //nolint:gocognit,gocyclo // one streaming pass over the XML tokens
	dec := xml.NewDecoder(r)
	dec.Strict = true
	var nodes arena
	root := nodes.node()
	root.Kind, root.Line, root.Column = RootNode, 1, 1
	root.root = root
	order := int32(1)
	cur := root
	// The names of elements and attributes repeat throughout a document:
	// every node shares one copy of each.
	names := map[string]string{}
	intern := func(s string) string {
		if s == "" {
			return ""
		}
		if v, ok := names[s]; ok {
			return v
		}
		names[s] = s
		return s
	}
	// bound maps each prefix in scope to its namespace. An element that
	// declares a prefix records in undo what the prefix was bound to
	// before, and marks holds, per open element, the length of undo when
	// it started: its end tag restores what it changed. A lookup does not
	// depend on how deep the element lies.
	type binding struct {
		prefix, uri string
		bound       bool
	}
	bound := map[string]string{"xml": NamespaceXML}
	var undo []binding
	var marks []int
	lookup := func(prefix string) (string, bool) {
		uri, ok := bound[prefix]
		return uri, ok || prefix == ""
	}
	// A text node is a run of character data and CDATA sections, which
	// arrive as a token each. The first piece is the Data of the node; a
	// second starts text, which collects the pieces until another token
	// ends the run. Appending each piece to Data would copy the text so
	// far for every section.
	var run *Node   // the text node that the last token added to
	var text []byte // its pieces, once there are more than one
	collecting := false
	endRun := func() {
		if collecting {
			run.Data = string(text)
			text, collecting = text[:0], false
		}
		run = nil
	}
	newNode := func(kind NodeKind, line, col int) (*Node, error) {
		if order > maxNodes {
			return nil, ErrTooManyNodes
		}
		n := nodes.node()
		n.Kind, n.Line, n.Column = kind, position(line), position(col)
		n.root = root
		n.order = order
		order++
		return n, nil
	}
	appendChild := func(parent, c *Node) {
		c.Parent = parent
		c.index = int32(len(parent.Children)) //nolint:gosec // fewer than maxNodes
		parent.Children = append(parent.Children, c)
	}

	for tokens := 0; ; tokens++ {
		if tokens%parseCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		line, col := dec.InputPos()
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if _, isText := tok.(xml.CharData); !isText {
			endRun()
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el, err := newNode(ElementNode, line, col)
			if err != nil {
				return nil, err
			}
			el.Prefix, el.Local = intern(t.Name.Space), intern(t.Name.Local)
			marks = append(marks, len(undo))
			declares := false
			for _, a := range t.Attr {
				var prefix string
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
				case a.Name.Space == "xmlns":
					prefix = intern(a.Name.Local)
				default:
					continue
				}
				declares = true
				uri := intern(a.Value)
				before, was := bound[prefix]
				undo = append(undo, binding{prefix: prefix, uri: before, bound: was})
				bound[prefix] = uri
				x := el.more()
				x.nsDecls = append(x.nsDecls, nsBinding{prefix, uri})
			}
			uri, ok := lookup(el.Prefix)
			if !ok {
				return nil, fmt.Errorf("line %d: undeclared namespace prefix %q", line, el.Prefix)
			}
			el.Space = uri
			appendChild(cur, el)
			if !declares && len(t.Attr) > 0 {
				// Sized at once: an element without namespace declarations
				// has an attribute node for each attribute.
				el.Attrs = make([]*Node, 0, len(t.Attr))
			}
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
					continue
				}
				at, err := newNode(AttributeNode, line, col)
				if err != nil {
					return nil, err
				}
				at.Prefix, at.Local, at.Data = intern(a.Name.Space), intern(a.Name.Local), a.Value
				at.Parent, at.index = el, int32(len(el.Attrs)) //nolint:gosec // fewer than maxNodes
				if at.Prefix != "" {
					if at.Space, ok = lookup(at.Prefix); !ok {
						return nil, fmt.Errorf("line %d: undeclared namespace prefix %q", line, at.Prefix)
					}
				}
				el.Attrs = append(el.Attrs, at)
			}
			cur = el
		case xml.EndElement:
			if cur.Kind != ElementNode || t.Name.Space != cur.Prefix || t.Name.Local != cur.Local {
				return nil, fmt.Errorf("line %d: unexpected end element %s", line, t.Name.Local)
			}
			mark := marks[len(marks)-1]
			for i := len(undo) - 1; i >= mark; i-- {
				if b := undo[i]; b.bound {
					bound[b.prefix] = b.uri
				} else {
					delete(bound, b.prefix)
				}
			}
			undo, marks = undo[:mark], marks[:len(marks)-1]
			cur.end = order
			cur = cur.Parent
		case xml.CharData:
			if cur.Kind == RootNode {
				continue // whitespace outside the document element is not a node
			}
			if len(t) == 0 {
				continue // an empty CDATA section: there are no empty text nodes
			}
			if run != nil {
				if !collecting {
					text, collecting = append(text[:0], run.Data...), true
				}
				text = append(text, t...)
				continue
			}
			node, err := newNode(TextNode, line, col)
			if err != nil {
				return nil, err
			}
			node.Data = string(t)
			appendChild(cur, node)
			run = node
		case xml.Comment:
			c, err := newNode(CommentNode, line, col)
			if err != nil {
				return nil, err
			}
			c.Data = string(t)
			appendChild(cur, c)
		case xml.ProcInst:
			if t.Target == "xml" {
				continue
			}
			pi, err := newNode(ProcessingInstructionNode, line, col)
			if err != nil {
				return nil, err
			}
			pi.Local, pi.Data = t.Target, strings.TrimLeft(string(t.Inst), " \t\r\n")
			appendChild(cur, pi)
		case xml.Directive:
			return nil, errors.New("DOCTYPE declarations are not supported")
		}
	}
	if cur != root {
		return nil, errors.New("unexpected end of document")
	}
	root.end = order
	return &Document{Root: root, Nodes: int(order) - 1}, nil
}
