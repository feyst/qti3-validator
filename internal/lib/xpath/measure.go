package xpath

import (
	"bytes"
	"unsafe"
)

// Size is what a document will hold once parsed: the nodes of each kind and
// the bytes of their content. Measure counts it without building anything,
// so a caller can tell what Parse will take in memory before it does.
type Size struct {
	Elements   int
	Attributes int // attribute nodes; namespace declarations are not nodes
	Texts      int // text nodes: a run of character data and CDATA sections
	Others     int // comments and processing instructions
	Namespaces int // namespace declarations
	// TextBytes are the bytes of the text nodes, comments and processing
	// instructions, and ValueBytes those of the attribute values, as written:
	// an entity reference counts by its length in the source, which is no
	// less than what it stands for.
	TextBytes  int64
	ValueBytes int64
	// Largest is the size of the largest single piece of content: Parse
	// holds it in a buffer of its own while it reads it.
	Largest int64
}

// Nodes is the number of nodes, as Document.Nodes counts them.
func (s Size) Nodes() int { return s.Elements + s.Attributes + s.Texts + s.Others }

// The memory of a parsed document, beside its nodes: a node is pointed to by
// its parent, in a slice that grows by doubling and so holds up to two
// pointers per child, and a string of a few bytes takes up to 16.
const (
	nodeBytes       = int64(unsafe.Sizeof(Node{}))
	childSlotBytes  = 2 * int64(unsafe.Sizeof((*Node)(nil)))
	attrSlotBytes   = int64(unsafe.Sizeof((*Node)(nil)))
	smallAllocBytes = 16
	// A namespace declaration is kept with its element: the extra part of
	// the node and a binding, in a slice that grows by doubling.
	namespaceBytes = int64(unsafe.Sizeof(nodeExtra{})) + 8 + 2*int64(unsafe.Sizeof(nsBinding{}))
)

// DOMBytes is an upper estimate of the memory the Document of Parse takes
// for a document of this size. It follows the layout of Node, so it changes
// with it; TestMeasureDOMBytes holds it to what Parse really takes.
func (s Size) DOMBytes() int64 {
	children := int64(s.Elements + s.Texts + s.Others)
	attrs := int64(s.Attributes)
	return int64(s.Nodes())*nodeBytes +
		children*childSlotBytes + attrs*attrSlotBytes +
		(s.TextBytes+s.ValueBytes)*9/8 + // a string takes up to an eighth more than its bytes
		int64(s.Texts+s.Others+s.Attributes)*smallAllocBytes +
		int64(s.Namespaces)*namespaceBytes
}

// Measure counts what Parse makes of data, in one pass that allocates
// nothing. It expects a well-formed document, as Parse does; for any other
// input the counts are no more than a guess, and never a failure.
func Measure(data []byte) Size { //nolint:gocognit // one pass over the markup
	var s Size
	depth := 0
	afterText := false // the node before this point is a text node of the same parent
	text := func(n int) {
		if n == 0 || depth == 0 {
			return // character data outside the document element is not a node
		}
		if !afterText {
			s.Texts++
			afterText = true
		}
		s.TextBytes += int64(n)
		s.Largest = max(s.Largest, int64(n))
	}
	for len(data) > 0 {
		lt := bytes.IndexByte(data, '<')
		if lt < 0 {
			text(len(data))
			break
		}
		text(lt)
		data = data[lt:]
		switch {
		case bytes.HasPrefix(data, []byte("<![CDATA[")):
			end := bytes.Index(data, []byte("]]>"))
			if end < 0 {
				return s
			}
			// A CDATA section is a text node even when it is empty.
			if depth > 0 && !afterText {
				s.Texts++
				afterText = true
			}
			text(end - len("<![CDATA["))
			data = data[end+3:]
		case bytes.HasPrefix(data, []byte("<!--")):
			end := bytes.Index(data[4:], []byte("-->"))
			if end < 0 {
				return s
			}
			s.Others++
			s.TextBytes += int64(end)
			s.Largest = max(s.Largest, int64(end))
			afterText = false
			data = data[4+end+3:]
		case bytes.HasPrefix(data, []byte("<?")):
			end := bytes.Index(data, []byte("?>"))
			if end < 0 {
				return s
			}
			if !isXMLDeclaration(data[2:end]) {
				s.Others++
				s.TextBytes += int64(end - 2)
				s.Largest = max(s.Largest, int64(end-2))
				afterText = false
			}
			data = data[end+2:]
		case bytes.HasPrefix(data, []byte("<!")):
			// A DOCTYPE, which Parse refuses.
			end := bytes.IndexByte(data, '>')
			if end < 0 {
				return s
			}
			data = data[end+1:]
		case bytes.HasPrefix(data, []byte("</")):
			end := bytes.IndexByte(data, '>')
			if end < 0 {
				return s
			}
			depth--
			afterText = false
			data = data[end+1:]
		default:
			rest, empty, ok := s.startTag(data[1:])
			if !ok {
				return s
			}
			if !empty {
				depth++
			}
			afterText = false
			data = rest
		}
	}
	return s
}

// isXMLDeclaration reports whether the content of a processing instruction
// is that of the XML declaration, which is not a node.
func isXMLDeclaration(pi []byte) bool {
	return bytes.HasPrefix(pi, []byte("xml")) && (len(pi) == 3 || isSpace(pi[3]))
}

// startTag counts the element whose start tag begins at tag, after its "<",
// and its attributes. It returns what follows the tag and whether the
// element is empty (ends in "/>"); ok is false when the tag does not end.
func (s *Size) startTag(tag []byte) (rest []byte, empty, ok bool) {
	s.Elements++
	i := 0
	for i < len(tag) && !isSpace(tag[i]) && tag[i] != '>' && tag[i] != '/' {
		i++
	}
	for {
		for i < len(tag) && isSpace(tag[i]) {
			i++
		}
		switch {
		case i >= len(tag):
			return nil, false, false
		case tag[i] == '>':
			return tag[i+1:], false, true
		case tag[i] == '/' && i+1 < len(tag) && tag[i+1] == '>':
			return tag[i+2:], true, true
		case tag[i] == '/':
			i++
		default:
			if i, ok = s.attribute(tag, i); !ok {
				return nil, false, false
			}
		}
	}
}

// attribute counts the attribute that starts at tag[i]: its name, "=" and
// its quoted value. It returns where the tag goes on; ok is false when the
// tag ends before the attribute does.
func (s *Size) attribute(tag []byte, i int) (next int, ok bool) {
	start := i
	for i < len(tag) && tag[i] != '=' && !isSpace(tag[i]) && tag[i] != '>' {
		i++
	}
	name := tag[start:i]
	for i < len(tag) && tag[i] != '"' && tag[i] != '\'' && tag[i] != '>' {
		i++
	}
	if i >= len(tag) {
		return i, false
	}
	if tag[i] == '>' {
		return i, true // no value, which is not well-formed: the tag ends here
	}
	end := bytes.IndexByte(tag[i+1:], tag[i])
	if end < 0 {
		return i, false
	}
	if bytes.Equal(name, []byte("xmlns")) || bytes.HasPrefix(name, []byte("xmlns:")) {
		s.Namespaces++
	} else {
		s.Attributes++
		s.ValueBytes += int64(end)
		s.Largest = max(s.Largest, int64(end))
	}
	return i + end + 2, true
}
