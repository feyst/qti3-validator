package qti

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// BaseType is the type of a QTI value.
type BaseType string

// The base types of QTI 3.
const (
	Identifier   BaseType = "identifier"
	Boolean      BaseType = "boolean"
	Integer      BaseType = "integer"
	Float        BaseType = "float"
	String       BaseType = "string"
	Point        BaseType = "point"
	Pair         BaseType = "pair"
	DirectedPair BaseType = "directedPair"
	Duration     BaseType = "duration"
	File         BaseType = "file"
	URI          BaseType = "uri"
)

// Cardinality is how many values a QTI variable holds.
type Cardinality string

// The cardinalities of QTI 3.
const (
	Single   Cardinality = "single"
	Multiple Cardinality = "multiple"
	Ordered  Cardinality = "ordered"
	Record   Cardinality = "record"
)

// IsNumeric reports whether values of b are numbers.
func (b BaseType) IsNumeric() bool { return b == Integer || b == Float }

// IsContainer reports whether c holds more than one value.
func (c Cardinality) IsContainer() bool { return c == Multiple || c == Ordered }

// xsDouble is the lexical space of xs:double, which QTI uses for float and
// duration values.
var xsDouble = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// ValidValue reports whether s is a value of base type b. Leading and
// trailing whitespace is ignored. An empty value is NULL ("Empty containers
// and empty strings are always treated as NULL values") and valid for any
// type. Values of the file and uri types, and of unknown types, are not
// checked.
func ValidValue(b BaseType, s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	switch b {
	case Identifier:
		return isNCName(s)
	case Boolean:
		return s == "true" || s == "false" || s == "1" || s == "0"
	case Integer:
		_, err := strconv.ParseInt(s, 10, 64)
		return err == nil
	case Float, Duration:
		return xsDouble.MatchString(s) || s == "INF" || s == "-INF" || s == "NaN"
	case Point:
		parts := strings.Fields(s)
		return len(parts) == 2 && ValidValue(Integer, parts[0]) && ValidValue(Integer, parts[1])
	case Pair, DirectedPair:
		parts := strings.Fields(s)
		return len(parts) == 2 && isNCName(parts[0]) && isNCName(parts[1])
	case String, File, URI:
		return true
	}
	return true
}

// isNCName reports whether s is an XML name without a colon, the lexical
// space of QTI identifiers.
func isNCName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r):
		case i > 0 && (unicode.IsDigit(r) || r == '-' || r == '.' || r == '·' ||
			unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r)):
		default:
			return false
		}
	}
	return true
}
