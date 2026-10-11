package schematron

import (
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"qti3-validator/internal/lib/xpath"
)

// sameAttributeCount matches the way rules check that an identifier is
// unique: count the elements of some kinds that have the identifier of the
// context node,
//
//	count(SCOPE//p:*[self::p:a or self::p:b][@identifier = current()/@identifier])
//
// where SCOPE is .., a variable or nothing (the document), the step is /
// or //, and its name test is * or p:*. Evaluated for each of n elements,
// each count walks the scope again, so the rule takes time n². Groups:
// the character before count, the scope, the step, its name test, the
// self:: tests, and the attribute names on both sides of the =.
var sameAttributeCount = regexp.MustCompile(`(^|[^\w.:$-])count\(\s*(\.\.|\$[\pL_][\w.-]*|)(//?)(\*|[\pL_][\w.-]*:\*)\[\s*(self::[^\[\]()'"]*?)\s*\]\[\s*@([\pL_][\w.-]*)\s*=\s*current\(\)/@([\pL_][\w.-]*)\s*\]\s*\)`)

// indexedCounts is turned off only by tests, to compare with plain XPath.
var indexedCounts = true

var (
	selfTestSeparator = regexp.MustCompile(`\s+or\s+`)
	selfTest          = regexp.MustCompile(`^self::(?:([\pL_][\w.-]*):)?([\pL_][\w.-]*)$`)
)

// indexCounts rewrites each such count in test into a call of
// count-by-attribute with prefix ext, bound to xpath.ExtensionNamespace,
// which returns the same number from an index of the document; see
// xpath.CountByAttribute. Anything else in the test, and a count in any
// other form, is left as it is.
func indexCounts(test string, ns map[string]string, ext string) string {
	if !indexedCounts {
		return test
	}
	matches := sameAttributeCount.FindAllStringSubmatchIndex(test, -1)
	if matches == nil {
		return test
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		group := func(i int) string { return test[m[2*i]:m[2*i+1]] }
		start := m[0] + len(group(1)) // keep the character before count
		call, ok := countByAttributeCall(ext, group(2), group(3), group(4), group(5), group(6), group(7), ns)
		if !ok || inLiteral(test[:start]) {
			continue
		}
		b.WriteString(test[last:start])
		b.WriteString(call)
		last = m[1]
	}
	b.WriteString(test[last:])
	return b.String()
}

// countByAttributeCall builds the call for the parts of a match, or returns
// false when it cannot: a prefix is not bound, the attribute names differ,
// or a name would not survive the string literal.
func countByAttributeCall(ext, scope, step, nameTest, selfTests, attr, currentAttr string, ns map[string]string) (string, bool) {
	if attr != currentAttr {
		return "", false
	}
	stepSpace, anySpace := "", true
	if prefix, ok := strings.CutSuffix(nameTest, ":*"); ok {
		uri, bound := ns[prefix]
		if !bound {
			return "", false
		}
		stepSpace, anySpace = uri, false
	}
	var names []string
	for _, t := range selfTestSeparator.Split(selfTests, -1) {
		m := selfTest.FindStringSubmatch(t)
		if m == nil {
			return "", false
		}
		space := "" // an unprefixed name has no namespace in XPath 1.0
		if m[1] != "" {
			uri, bound := ns[m[1]]
			if !bound || strings.ContainsAny(uri, "{}' \t\r\n") {
				return "", false
			}
			space = uri
		}
		if !anySpace && space != stepSpace {
			continue // the step's name test excludes it
		}
		names = append(names, "{"+space+"}"+m[2])
	}
	axis := "child"
	if step == "//" {
		axis = "descendant"
	}
	if scope == "" {
		scope = "/"
	}
	return ext + ":" + xpath.CountByAttribute + "(" + scope + ", '" + axis + "', '" + strings.Join(names, " ") + "', '" + attr + "', current()/@" + attr + ")", true
}

// extensionPrefix returns the namespaces with a prefix bound to
// xpath.ExtensionNamespace, and that prefix: one the namespaces bind to it
// already, or else qtiv or, when that is taken, the first of qtiv2, qtiv3,
// ... that is free. The namespaces are not changed.
func extensionPrefix(ns map[string]string) (map[string]string, string) {
	for _, p := range slices.Sorted(maps.Keys(ns)) {
		if ns[p] == xpath.ExtensionNamespace {
			return ns, p
		}
	}
	prefix := "qtiv"
	for i := 2; ; i++ {
		if _, taken := ns[prefix]; !taken {
			break
		}
		prefix = "qtiv" + strconv.Itoa(i)
	}
	out := make(map[string]string, len(ns)+1)
	maps.Copy(out, ns)
	out[prefix] = xpath.ExtensionNamespace
	return out, prefix
}

// inLiteral reports whether s, the start of an expression, ends inside a
// string literal.
func inLiteral(s string) bool {
	var quote rune
	for _, c := range s {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		}
	}
	return quote != 0
}
