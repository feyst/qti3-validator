package schematron

import (
	"fmt"
	"maps"
	"regexp"
	"strings"

	"qti3-validator/internal/lib/xpath"
)

// Engine runs compiled rules. It is immutable and safe for concurrent use.
type Engine struct {
	sets []*engineSet
}

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
		l := loader{ns: cs.Namespaces, cache: map[string]*xpath.Expr{}, vars: map[string]bool{}}
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

// Validate runs every pattern on doc and returns up to maxFailures
// failures (0: no limit), in pattern order and then document order.
func (e *Engine) Validate(doc *xpath.Document, maxFailures int) ([]Failure, error) { //nolint:gocognit // the evaluation loop of ISO Schematron; tested against the reference implementation
	var index map[expandedName][]*xpath.Node
	var failures []Failure
	for _, es := range e.sets {
		vars, err := bindLets(es.lets, doc.Root, map[string]xpath.Value{})
		if err != nil {
			return failures, err
		}
		for _, p := range es.patterns {
			pvars := vars
			if len(p.lets) > 0 {
				if pvars, err = bindLets(p.lets, doc.Root, maps.Clone(vars)); err != nil {
					return failures, err
				}
			}
			// First-match bookkeeping is only needed with several rules.
			var seen map[*xpath.Node]bool
			if len(p.rules) > 1 {
				seen = map[*xpath.Node]bool{}
			}
			for _, r := range p.rules {
				var nodes []*xpath.Node
				if r.byName != nil {
					if index == nil {
						index = indexElements(doc.Root)
					}
					nodes = index[*r.byName]
				} else if nodes, err = r.context.Select(xpath.Context{Node: doc.Root, Variables: pvars}); err != nil {
					return failures, err
				}
				for _, n := range nodes {
					if seen != nil {
						if seen[n] {
							continue // within a pattern only the first matching rule applies
						}
						seen[n] = true
					}
					rvars := pvars
					if len(r.lets) > 0 {
						if rvars, err = bindLets(r.lets, n, maps.Clone(pvars)); err != nil {
							return failures, err
						}
					}
					for _, a := range r.assertions {
						fired, err := a.fires(n, rvars)
						if err != nil {
							return failures, err
						}
						if !fired {
							continue
						}
						msg, err := a.render(n, rvars)
						if err != nil {
							return failures, err
						}
						failures = append(failures, Failure{
							Source: es.source, Pattern: p.id, Assertion: a.id, Report: a.report,
							Role: a.role, Flag: a.flag, Warning: warningRoles[strings.ToLower(a.role)],
							Message: msg, Node: n,
						})
						if maxFailures > 0 && len(failures) >= maxFailures {
							return failures, nil
						}
					}
				}
			}
		}
	}
	return failures, nil
}

func (a engineAssertion) fires(n *xpath.Node, vars map[string]xpath.Value) (bool, error) {
	var ok bool
	if a.native != nil {
		ok = a.native.holds(n)
	} else {
		var err error
		if ok, err = a.test.EvaluateBool(xpath.Context{Node: n, Variables: vars}); err != nil {
			return false, err
		}
	}
	return ok == a.report, nil
}

func (a engineAssertion) render(n *xpath.Node, vars map[string]xpath.Value) (string, error) {
	var b strings.Builder
	for _, p := range a.message {
		if p.selectExpr == nil {
			b.WriteString(p.text)
			continue
		}
		s, err := p.selectExpr.EvaluateString(xpath.Context{Node: n, Variables: vars})
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return b.String(), nil
}

func bindLets(lets []engineLet, n *xpath.Node, vars map[string]xpath.Value) (map[string]xpath.Value, error) {
	for _, l := range lets {
		v, err := l.expr.Evaluate(xpath.Context{Node: n, Variables: vars})
		if err != nil {
			return nil, err
		}
		vars[l.name] = v
	}
	return vars, nil
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
