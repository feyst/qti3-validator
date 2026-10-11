package xpath

import (
	"strings"
	"testing"
)

// qtiv:count-by-attribute must return what the count it stands for
// returns, for every element as context node, including scopes of several
// nodes whose subtrees overlap.
func TestCountByAttributeMatchesXPath(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r xmlns="urn:q" xmlns:x="urn:x">
		<g id="a"><a id="a"/><b id="a"><a id="a"/><a id="b"/><x:a id="a"/></b><a x:id="a"/><a/><a id=""/><a id=""/></g>
		<g id="b"><b id="b"><g><a id="b"/></g></b><a id="a"/><c id="a"/>text<!--c--></g>
	</r>`))
	if err != nil {
		t.Fatal(err)
	}
	ns := map[string]string{"q": "urn:q", "x": "urn:x"}
	for _, pair := range [][2]string{
		{"count(..//q:*[self::q:a or self::q:b][@id = current()/@id])", "qtiv:count-by-attribute(.., 'descendant', '{urn:q}a {urn:q}b', 'id', current()/@id)"},
		{"count(../*[self::q:a or self::x:a][@id = current()/@id])", "qtiv:count-by-attribute(.., 'child', '{urn:q}a {urn:x}a', 'id', current()/@id)"},
		{"count(//*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(/, 'descendant', '{urn:q}a', 'id', current()/@id)"},
		{"count(/*[self::q:r][@id = current()/@id])", "qtiv:count-by-attribute(/, 'child', '{urn:q}r', 'id', current()/@id)"},
		{"count(ancestor-or-self::*//*[self::q:a or self::b][@id = current()/@id])", "qtiv:count-by-attribute(ancestor-or-self::*, 'descendant', '{urn:q}a {}b', 'id', current()/@id)"},
		{"count(ancestor-or-self::*/*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(ancestor-or-self::*, 'child', '{urn:q}a', 'id', current()/@id)"},
		{"count(//q:g//*[self::q:a or self::q:c][@id = current()/@id])", "qtiv:count-by-attribute(//q:g, 'descendant', '{urn:q}a {urn:q}c', 'id', current()/@id)"},
		{"count(//q:g//*[self::q:a][@id = current()/@x:id])", "qtiv:count-by-attribute(//q:g, 'descendant', '{urn:q}a', 'id', current()/@x:id)"},
		{"count(//q:none//*[self::q:a][@id = current()/@id])", "qtiv:count-by-attribute(//q:none, 'descendant', '{urn:q}a', 'id', current()/@id)"},
	} {
		plain, err := Compile(pair[0], Options{Namespaces: ns})
		if err != nil {
			t.Fatal(err)
		}
		indexed, err := Compile(pair[1], Options{Namespaces: ns})
		if err != nil {
			t.Fatal(err)
		}
		var walk func(n *Node)
		walk = func(n *Node) {
			ctx := Context{Node: n}
			want, err := plain.Evaluate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			got, err := indexed.Evaluate(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s on %s: indexed %v, XPath %v", pair[0], n.Path(), got, want)
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(doc.Root)
	}
}

func TestCountByAttributeErrors(t *testing.T) {
	doc, err := Parse(strings.NewReader(`<r id="a"/>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		"qtiv:count-by-attribute('x', 'child', 'r', 'id', @id)",
		"qtiv:count-by-attribute(/, 'parent', 'r', 'id', @id)",
		"qtiv:count-by-attribute(/, 'child', 'r', 'id', 'a')",
	} {
		e, err := Compile(src, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.Evaluate(Context{Node: doc.Root.Children[0]}); err == nil {
			t.Errorf("%s: want an error", src)
		}
	}
}
