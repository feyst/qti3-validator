package schematron

import (
	"context"
	"fmt"
	"maps"
	"math/bits"
	"regexp"
	"slices"
	"strings"

	"qti3-validator/internal/lib/xpath"
)

// Engine runs compiled rules. It is immutable and safe for concurrent use.
type Engine struct {
	sets []*engineSet
	// namespaceAxis reports that a rule has a step on the namespace axis.
	namespaceAxis bool
}

// UsesNamespaceAxis reports whether an expression of the rules has a step
// on the namespace axis, which takes memory beside the document's: see
// xpath.Expr.UsesNamespaceAxis.
func (e *Engine) UsesNamespaceAxis() bool { return e.namespaceAxis }

// Failure is a failed assert or a fired report.
type Failure struct {
	Source    string // schema the rule came from
	Pattern   string
	Assertion string // assertion id, if any
	Report    bool
	Role      string
	Flag      string
	Warning   bool // role marks the rule as a warning or information
	Message   string
	Node      *xpath.Node
}

// warningRoles are role values that mark a rule as not affecting validity.
var warningRoles = map[string]bool{"warning": true, "warn": true, "info": true, "information": true, "informational": true}

type engineSet struct {
	source   string
	lets     []engineLet
	patterns []enginePattern
}

type engineLet struct {
	name string
	expr *xpath.Expr
}

type enginePattern struct {
	id    string
	lets  []engineLet
	rules []engineRule
}

type engineRule struct {
	context    *xpath.Expr
	byName     *expandedName // context is //name: use the element index
	lets       []engineLet
	assertions []engineAssertion
}

type expandedName struct{ space, local string }

type engineAssertion struct {
	id, role, flag string
	report         bool
	test           *xpath.Expr
	native         *nameCheck
	message        []enginePart
}

type enginePart struct {
	text       string
	selectExpr *xpath.Expr
}

// Compile precompiles rule sets and loads them; see Precompile and New.
func Compile(sets ...RuleSet) (*Engine, error) {
	c, err := Precompile(sets...)
	if err != nil {
		return nil, err
	}
	return New(c)
}

// New loads precompiled rules. Identical expressions are parsed once.
func New(c *Compiled) (*Engine, error) {
	if c.Version != CompiledVersion {
		return nil, fmt.Errorf("compiled rules have format version %d, want %d", c.Version, CompiledVersion)
	}
	lists := make([]map[string]bool, len(c.NameLists))
	for i, names := range c.NameLists {
		lists[i] = make(map[string]bool, len(names))
		for _, n := range names {
			lists[i][n] = true
		}
	}
	e := &Engine{}
	for _, cs := range c.Sets {
		l := loader{ns: cs.Namespaces, cache: map[string]*xpath.Expr{}, vars: map[string]bool{}, namespaceAxis: &e.namespaceAxis}
		es := &engineSet{source: cs.Source}
		var err error
		if es.lets, err = l.lets(cs.Lets, l.vars); err != nil {
			return nil, err
		}
		for _, p := range cs.Patterns {
			ep := enginePattern{id: p.ID}
			pvars := maps.Clone(l.vars)
			if ep.lets, err = l.lets(p.Lets, pvars); err != nil {
				return nil, err
			}
			for _, r := range p.Rules {
				er, err := l.rule(r, pvars, lists)
				if err != nil {
					return nil, fmt.Errorf("schematron %s, pattern %q: %w", cs.Source, p.ID, err)
				}
				ep.rules = append(ep.rules, er)
			}
			es.patterns = append(es.patterns, ep)
		}
		e.sets = append(e.sets, es)
	}
	return e, nil
}

type loader struct {
	ns    map[string]string
	cache map[string]*xpath.Expr
	vars  map[string]bool
	// namespaceAxis is set when an expression uses the namespace axis.
	namespaceAxis *bool
}

// compile parses an expression, reusing an earlier parse of the same
// source. Variables were checked by Precompile, so all are allowed here.
func (l loader) compile(src string, vars map[string]bool) (*xpath.Expr, error) {
	if e, ok := l.cache[src]; ok {
		return e, nil
	}
	e, err := xpath.Compile(src, xpath.Options{Namespaces: l.ns, Variables: vars})
	if err != nil {
		return nil, err
	}
	if e.UsesNamespaceAxis() {
		*l.namespaceAxis = true
	}
	if !strings.Contains(src, "$") {
		l.cache[src] = e
	}
	return e, nil
}

func (l loader) lets(lets []Let, vars map[string]bool) ([]engineLet, error) {
	var out []engineLet
	for _, let := range lets {
		e, err := l.compile(let.Value, vars)
		if err != nil {
			return nil, err
		}
		out = append(out, engineLet{name: let.Name, expr: e})
		vars[let.Name] = true
	}
	return out, nil
}

var simpleContext = regexp.MustCompile(`^//(?:([\w.\-]+):)?([\w.\-]+)$`)

func (l loader) rule(r CompiledRule, patternVars map[string]bool, lists []map[string]bool) (engineRule, error) {
	var er engineRule
	var err error
	if er.context, err = l.compile(r.Context, patternVars); err != nil {
		return er, err
	}
	if m := simpleContext.FindStringSubmatch(r.Context); m != nil {
		space := ""
		if m[1] != "" {
			space = l.ns[m[1]]
		}
		er.byName = &expandedName{space: space, local: m[2]}
	}
	vars := maps.Clone(patternVars)
	if er.lets, err = l.lets(r.Lets, vars); err != nil {
		return er, err
	}
	for _, a := range r.Assertions {
		ea := engineAssertion{id: a.ID, role: a.Role, flag: a.Flag, report: a.Report}
		if an := a.AttributeName; an != nil {
			if an.Names < 0 || an.Names >= len(lists) {
				return er, fmt.Errorf("name list %d out of range", an.Names)
			}
			ea.native = &nameCheck{position: an.Position, empty: an.Empty, names: lists[an.Names], prefixes: an.Prefixes}
		} else if ea.test, err = l.compile(a.Test, vars); err != nil {
			return er, err
		}
		for _, part := range a.Message {
			ep := enginePart{text: part.Text}
			if part.Select != "" {
				if ep.selectExpr, err = l.compile(part.Select, vars); err != nil {
					return er, err
				}
			}
			ea.message = append(ea.message, ep)
		}
		er.assertions = append(er.assertions, ea)
	}
	return er, nil
}

// Limits bound the failures Validate returns. Warnings (see
// Failure.Warning) and errors are bounded apart, so warnings cannot use up
// the room for errors.
type Limits struct {
	// Errors is the number of failures that are not warnings after which
	// Validate stops; zero means no limit.
	Errors int
	// Warnings is the number of warnings Validate keeps; further warnings
	// are dropped and counted (see Result.OmittedWarnings), but validation
	// goes on. Zero means no limit; a negative number keeps none.
	Warnings int
}

// Result is what Check found.
type Result struct {
	// Failures are the failures within the limits, in pattern order and
	// then document order of their nodes; those of one node are in the
	// order of the assertions of its rule.
	Failures []Failure
	// OmittedWarnings is the number of warnings that fired after
	// Limits.Warnings were kept, until the validation ended. They are
	// counted when their assertion fires, so their messages are not
	// rendered.
	OmittedWarnings int
}

// Validate is Check without the count of omitted warnings.
func (e *Engine) Validate(s *xpath.Session, doc *xpath.Document, limits Limits) ([]Failure, error) {
	res, err := e.Check(s, doc, limits)
	return res.Failures, err
}

// Check runs every pattern on doc and returns the failures within limits,
// and how many warnings it left out.
//
// Every expression is evaluated in session s, which may stop the validation
// with an error wrapping xpath.ErrTooComplex or the error of its context;
// the failures found until then are returned with it. One session should
// serve one document, so the paths it remembers are reused across the rules;
// nil runs without limits in a session of its own.
func (e *Engine) Check(s *xpath.Session, doc *xpath.Document, limits Limits) (Result, error) { //nolint:gocognit // the evaluation loop of ISO Schematron; tested against the reference implementation
	if s == nil {
		s = xpath.NewSession(context.Background(), 0)
	}
	index := &elementIndex{root: doc.Root}
	var res Result
	errorCount, warningCount := 0, 0
	for _, es := range e.sets {
		vars, err := bindLets(s, es.lets, doc.Root, map[string]xpath.Value{})
		if err != nil {
			return res, err
		}
		constant(s, vars)
		for _, p := range es.patterns {
			pvars := vars
			if len(p.lets) > 0 {
				if pvars, err = bindLets(s, p.lets, doc.Root, maps.Clone(vars)); err != nil {
					return res, err
				}
				constant(s, pvars)
			}
			// A context that fails, as when the session runs out of steps,
			// still leaves the nodes of the rules before it: they are
			// checked, so their failures are among those found so far, and
			// the error is returned after them.
			matches, contextErr := p.matches(s, doc, pvars, index)
			for _, m := range matches {
				n, r := m.node, m.rule
				rvars := pvars
				var release []func()
				if len(r.lets) > 0 {
					// The lets of a rule keep their values while its later
					// lets and its assertions are evaluated on this node.
					rvars = maps.Clone(pvars)
					for i := range r.lets {
						if _, err = bindLets(s, r.lets[i:i+1], n, rvars); err != nil {
							return res, err
						}
						release = append(release, s.Scoped(rvars[r.lets[i].name]))
					}
				}
				for _, a := range r.assertions {
					fired, err := a.fires(s, n, rvars)
					if err != nil {
						return res, err
					}
					if !fired {
						continue
					}
					warning := warningRoles[strings.ToLower(a.role)]
					if warning && (limits.Warnings < 0 || limits.Warnings > 0 && warningCount >= limits.Warnings) {
						res.OmittedWarnings++
						continue // no room for more warnings; errors are still looked for
					}
					msg, err := a.render(s, n, rvars)
					if err != nil {
						return res, err
					}
					if warning {
						warningCount++
					}
					res.Failures = append(res.Failures, Failure{
						Source: es.source, Pattern: p.id, Assertion: a.id, Report: a.report,
						Role: a.role, Flag: a.flag, Warning: warning,
						Message: msg, Node: n,
					})
					if warning {
						continue
					}
					errorCount++
					if limits.Errors > 0 && errorCount >= limits.Errors {
						// The limit ends the validation whether or not the
						// context of a later rule failed.
						return res, nil
					}
				}
				for _, done := range release {
					done()
				}
			}
			if contextErr != nil {
				return res, contextErr
			}
		}
	}
	return res, nil
}

// match is a node and the rule of a pattern that applies to it.
type match struct {
	node *xpath.Node
	rule *engineRule
}

// matches returns the nodes that the rules of the pattern apply to, in
// document order, each with the first rule whose context matches it: within
// a pattern a node is checked by one rule only (ISO Schematron, 6.5). The
// contexts are evaluated in rule order, all of them before the first node
// is checked. When a context fails, the matches of the rules before it are
// returned with the error. Merging the nodes of several rules is charged to
// the session, a step per comparison.
func (p *enginePattern) matches(s *xpath.Session, doc *xpath.Document, vars map[string]xpath.Value, index *elementIndex) ([]match, error) {
	var out []match
	// First-match bookkeeping is only needed with several rules.
	var seen map[*xpath.Node]bool
	if len(p.rules) > 1 {
		seen = map[*xpath.Node]bool{}
	}
	for i := range p.rules {
		r := &p.rules[i]
		var nodes []*xpath.Node
		if r.byName != nil {
			nodes = index.named(*r.byName)
		} else {
			var err error
			if nodes, err = r.context.Select(xpath.Context{Node: doc.Root, Variables: vars, Session: s}); err != nil {
				sorted, _ := inDocumentOrder(s, out) // the context's error comes first
				return sorted, err
			}
		}
		for _, n := range nodes {
			if seen != nil {
				if seen[n] {
					continue
				}
				seen[n] = true
			}
			out = append(out, match{node: n, rule: r})
		}
	}
	return inDocumentOrder(s, out)
}

// inDocumentOrder sorts matches, the nodes of each rule in document order
// one after the other, into document order. A node occurs once, so the
// order is total. Sorting m matches that are not in order already takes
// about m·log2(m) comparisons, which are charged to the session before it
// starts; when the session stops there, it returns the session's error and
// the longest prefix that is in order, matches of the first rule.
func inDocumentOrder(s *xpath.Session, matches []match) ([]match, error) {
	byDocumentOrder := func(a, b match) int {
		if a.node.Before(b.node) {
			return -1
		}
		return 1
	}
	if slices.IsSortedFunc(matches, byDocumentOrder) {
		return matches, nil
	}
	if err := s.Charge(len(matches) * bits.Len(uint(len(matches)))); err != nil {
		ordered := 1
		for ordered < len(matches) && matches[ordered-1].node.Before(matches[ordered].node) {
			ordered++
		}
		return matches[:ordered], err
	}
	slices.SortFunc(matches, byDocumentOrder)
	return matches, nil
}

func (a engineAssertion) fires(sess *xpath.Session, n *xpath.Node, vars map[string]xpath.Value) (bool, error) {
	var ok bool
	if a.native != nil {
		ok = a.native.holds(n)
	} else {
		var err error
		if ok, err = a.test.EvaluateBool(xpath.Context{Node: n, Variables: vars, Session: sess}); err != nil {
			return false, err
		}
	}
	return ok == a.report, nil
}

func (a engineAssertion) render(sess *xpath.Session, n *xpath.Node, vars map[string]xpath.Value) (string, error) {
	var b strings.Builder
	for _, p := range a.message {
		if p.selectExpr == nil {
			b.WriteString(p.text)
			continue
		}
		s, err := p.selectExpr.EvaluateString(xpath.Context{Node: n, Variables: vars, Session: sess})
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

func bindLets(sess *xpath.Session, lets []engineLet, n *xpath.Node, vars map[string]xpath.Value) (map[string]xpath.Value, error) {
	for _, l := range lets {
		v, err := l.expr.Evaluate(xpath.Context{Node: n, Variables: vars, Session: sess})
		if err != nil {
			return nil, err
		}
		vars[l.name] = v
	}
	return vars, nil
}

// constant tells the session that the variables of a schema or a pattern,
// bound once per document, keep their values.
func constant(s *xpath.Session, vars map[string]xpath.Value) {
	for _, v := range vars {
		s.Constant(v)
	}
}

// elementIndex is the index of the elements of a document, built on first
// use.
type elementIndex struct {
	root   *xpath.Node
	byName map[expandedName][]*xpath.Node
}

// named returns the elements with the name, in document order.
func (x *elementIndex) named(name expandedName) []*xpath.Node {
	if x.byName == nil {
		x.byName = indexElements(x.root)
	}
	return x.byName[name]
}

// indexElements lists the elements of a document by expanded name, in
// document order.
func indexElements(root *xpath.Node) map[expandedName][]*xpath.Node {
	index := map[expandedName][]*xpath.Node{}
	var walk func(*xpath.Node)
	walk = func(n *xpath.Node) {
		for _, c := range n.Children {
			if c.Kind == xpath.ElementNode {
				k := expandedName{c.Space, c.Local}
				index[k] = append(index[k], c)
				walk(c)
			}
		}
	}
	walk(root)
	return index
}
