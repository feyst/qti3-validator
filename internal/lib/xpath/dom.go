// Package xpath implements XPath 1.0 (W3C Recommendation, 16 November 1999)
// over a small read-only DOM, plus the XSLT 1.0 functions current() and
// generate-id() that Schematron rules commonly use.
//
// Compiled expressions are immutable and safe for concurrent use; a Document
// is not, and belongs to one goroutine.
package xpath

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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
	Kind   NodeKind
	Prefix string
	Local  string
	Space  string
	Data   string // attribute value, text, comment, PI data or namespace URI
	Parent *Node
	// Children of the root or an element, attributes of an element.
	Children []*Node
	Attrs    []*Node
	// Line and Column of the start of the node in the source (1-based).
	// Attributes report the position of their element.
	Line, Column int

	index   int // position in the parent's Children or Attrs
	order   int // document order; attributes follow their element
	nsOrder int // namespace nodes: 1-based position among the owner's
	nsDecls []nsBinding
	nsNodes []*Node // built on first use of the namespace axis
}

type nsBinding struct{ prefix, uri string }

// Document is a parsed XML document.
type Document struct {
	Root *Node
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
	switch n.Kind {
	case RootNode, ElementNode:
		var b strings.Builder
		n.appendText(&b)
		return b.String()
	}
	return n.Data
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

// before reports whether a precedes b in document order.
func before(a, b *Node) bool {
	if a.order != b.order {
		return a.order < b.order
	}
	return a.nsOrder < b.nsOrder
}

// namespaces returns the namespace nodes of an element: one per in-scope
// prefix, the xml prefix included.
func (n *Node) namespaces() []*Node {
	if n.Kind != ElementNode {
		return nil
	}
	if n.nsNodes != nil {
		return n.nsNodes
	}
	scope := map[string]string{}
	var prefixes []string
	for e := n; e != nil && e.Kind == ElementNode; e = e.Parent {
		for _, d := range e.nsDecls {
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
			Kind: NamespaceNode, Local: p, Data: scope[p], Parent: n,
			order: n.order, Line: n.Line, Column: n.Column,
		})
	}
	for i, ns := range nodes {
		ns.index, ns.nsOrder = i, i+1
	}
	n.nsNodes = nodes
	return nodes
}

// Parse reads a document into the XPath data model. It accepts no DTD:
// documents are expected to have passed the validator's checks, and a
// DOCTYPE is rejected here as well.
func Parse(r io.Reader) (*Document, error) { //nolint:gocognit // one streaming pass over the XML tokens
	dec := xml.NewDecoder(r)
	dec.Strict = true
	root := &Node{Kind: RootNode, Line: 1, Column: 1}
	order := 1
	cur := root
	scopes := []map[string]string{{"xml": NamespaceXML}}
	lookup := func(prefix string) (string, bool) {
		for i := len(scopes) - 1; i >= 0; i-- {
			if uri, ok := scopes[i][prefix]; ok {
				return uri, true
			}
		}
		return "", prefix == ""
	}
	appendChild := func(parent, c *Node) {
		c.Parent = parent
		c.index = len(parent.Children)
		c.order = order
		order++
		parent.Children = append(parent.Children, c)
	}

	for {
		line, col := dec.InputPos()
		tok, err := dec.RawToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &Node{Kind: ElementNode, Prefix: t.Name.Space, Local: t.Name.Local, Line: line, Column: col}
			bindings := map[string]string{}
			for _, a := range t.Attr {
				switch {
				case a.Name.Space == "" && a.Name.Local == "xmlns":
					bindings[""] = a.Value
					el.nsDecls = append(el.nsDecls, nsBinding{"", a.Value})
				case a.Name.Space == "xmlns":
					bindings[a.Name.Local] = a.Value
					el.nsDecls = append(el.nsDecls, nsBinding{a.Name.Local, a.Value})
				}
			}
			scopes = append(scopes, bindings)
			uri, ok := lookup(el.Prefix)
			if !ok {
				return nil, fmt.Errorf("line %d: undeclared namespace prefix %q", line, el.Prefix)
			}
			el.Space = uri
			appendChild(cur, el)
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
					continue
				}
				at := &Node{
					Kind: AttributeNode, Prefix: a.Name.Space, Local: a.Name.Local, Data: a.Value,
					Parent: el, index: len(el.Attrs), order: order, Line: line, Column: col,
				}
				order++
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
			scopes = scopes[:len(scopes)-1]
			cur = cur.Parent
		case xml.CharData:
			if cur.Kind == RootNode {
				continue // whitespace outside the document element is not a node
			}
			if n := len(cur.Children); n > 0 && cur.Children[n-1].Kind == TextNode {
				cur.Children[n-1].Data += string(t)
				continue
			}
			appendChild(cur, &Node{Kind: TextNode, Data: string(t), Line: line, Column: col})
		case xml.Comment:
			appendChild(cur, &Node{Kind: CommentNode, Data: string(t), Line: line, Column: col})
		case xml.ProcInst:
			if t.Target == "xml" {
				continue
			}
			appendChild(cur, &Node{
				Kind: ProcessingInstructionNode, Local: t.Target,
				Data: strings.TrimLeft(string(t.Inst), " \t\r\n"), Line: line, Column: col,
			})
		case xml.Directive:
			return nil, errors.New("DOCTYPE declarations are not supported")
		}
	}
	if cur != root {
		return nil, errors.New("unexpected end of document")
	}
	return &Document{Root: root}, nil
}
