package schematron

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/kennisnet/qti3-validator/internal/xpath"
)

// parsedNameTest is a test recognised as an attribute-name check; see
// AttributeName.
type parsedNameTest struct {
	position int
	empty    bool
	names    []string // sorted, unique
	prefixes []string
}

var (
	nameLengthTerm = regexp.MustCompile(`^string-length\(name\(@\*\[(\d+)\]\)\) ?= ?0$`)
	nameEqualsTerm = regexp.MustCompile(`^(?:string\(name\(@\*\[(\d+)\]\)\)|name\(@\*\[(\d+)\]\)) ?= ?'([^']*)'$`)
	nameStartsTerm = regexp.MustCompile(`^starts-with\(name\(@\*\[(\d+)\]\), ?'([^']*)'\)$`)
)

// parseAttributeNameCheck recognises a test that is exactly a disjunction of
// the three term forms above, all about the same attribute position. Any
// other test returns nil and is evaluated as XPath.
func parseAttributeNameCheck(test string) *parsedNameTest {
	c := &parsedNameTest{}
	for _, term := range strings.Split(test, " or ") {
		var pos string
		switch {
		case nameLengthTerm.MatchString(term):
			pos = nameLengthTerm.FindStringSubmatch(term)[1]
			c.empty = true
		case nameEqualsTerm.MatchString(term):
			m := nameEqualsTerm.FindStringSubmatch(term)
			pos = m[1] + m[2] // one of the two is set
			c.names = append(c.names, m[3])
		case nameStartsTerm.MatchString(term):
			m := nameStartsTerm.FindStringSubmatch(term)
			pos = m[1]
			c.prefixes = append(c.prefixes, m[2])
		default:
			return nil
		}
		n, err := strconv.Atoi(pos)
		if err != nil || n < 1 || c.position != 0 && n != c.position {
			return nil
		}
		c.position = n
	}
	slices.Sort(c.names)
	c.names = slices.Compact(c.names)
	return c
}

// nameCheck is the runtime form of AttributeName.
type nameCheck struct {
	position int
	empty    bool
	names    map[string]bool // shared between checks with the same list
	prefixes []string
}

// holds evaluates the test on node n.
func (c *nameCheck) holds(n *xpath.Node) bool {
	name := ""
	if c.position <= len(n.Attrs) {
		name = n.Attrs[c.position-1].Name()
	}
	if name == "" {
		// string-length('')=0; '' = 'x' only for x = ''; starts-with('', p) only for p = ''.
		return c.empty || c.names[""] || slices.Contains(c.prefixes, "")
	}
	if c.names[name] {
		return true
	}
	for _, p := range c.prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
