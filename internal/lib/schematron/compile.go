package schematron

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"qti3-validator/internal/lib/xpath"
)

// Compiled is the build-time form of rule sets: abstract patterns are
// instantiated, extends and diagnostics resolved, rule contexts turned into
// XPath, and generated attribute-name tests turned into name lists. Every
// expression in it has been compiled once, so loading it cannot fail on a
// rule. It is what the service embeds; New turns it into an Engine.
type Compiled struct {
	Version   int           `json:"version"`
	NameLists [][]string    `json:"name_lists,omitempty"` // shared by attribute-name checks
	Sets      []CompiledSet `json:"sets"`
}

// Subset returns the rule sets from the given sources, in their original
// order, sharing the name lists. It fails if a source has no rule set, so a
// missing rule set cannot go unnoticed.
func (c *Compiled) Subset(sources ...string) (*Compiled, error) {
	out := &Compiled{Version: c.Version, NameLists: c.NameLists}
	for _, src := range sources {
		found := false
		for _, set := range c.Sets {
			if set.Source == src {
				out.Sets = append(out.Sets, set)
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no Schematron rules for %s", src)
		}
	}
	return out, nil
}

// CompiledVersion changes when the Compiled format does.
const CompiledVersion = 1

// CompiledSet is a compiled rule set: one schema's patterns.
type CompiledSet struct {
	Source     string            `json:"source"`
	Namespaces map[string]string `json:"namespaces"`
	Lets       []Let             `json:"lets,omitempty"`
	Patterns   []CompiledPattern `json:"patterns"`
}

// CompiledPattern is a compiled pattern, abstract patterns instantiated.
type CompiledPattern struct {
	ID    string         `json:"id,omitempty"`
	Lets  []Let          `json:"lets,omitempty"`
	Rules []CompiledRule `json:"rules"`
}

// CompiledRule is a compiled rule, extends resolved.
type CompiledRule struct {
	Context    string              `json:"context"` // XPath selecting the nodes the rule applies to
	Lets       []Let               `json:"lets,omitempty"`
	Assertions []CompiledAssertion `json:"assertions"`
}

// CompiledAssertion is a compiled assert or report, diagnostics resolved.
type CompiledAssertion struct {
	ID     string `json:"id,omitempty"`
	Report bool   `json:"report,omitempty"`
	Role   string `json:"role,omitempty"`
	Flag   string `json:"flag,omitempty"`
	// Exactly one of Test and AttributeName is set.
	Test          string         `json:"test,omitempty"`
	AttributeName *AttributeName `json:"attribute_name,omitempty"`
	Message       []MessagePart  `json:"message"` // diagnostics appended
}

// AttributeName is the native form of a generated test of the form
//
//	string-length(name(@*[N]))=0 or string(name(@*[N]))='a' or ...
//	    or starts-with(name(@*[N]), 'data-')
//
// The QTI 3 schema has thousands of these, each with dozens of name() calls.
type AttributeName struct {
	Position int      `json:"position"`        // N, 1-based
	Empty    bool     `json:"empty,omitempty"` // passes when there is no attribute N
	Names    int      `json:"names"`           // index in Compiled.NameLists
	Prefixes []string `json:"prefixes,omitempty"`
}

// Precompile resolves and checks rule sets for New. Rule sets without
// patterns are dropped.
func Precompile(sets ...RuleSet) (*Compiled, error) {
	c := &Compiled{Version: CompiledVersion}
	lists := map[string]int{}
	for _, rs := range sets {
		if len(rs.Patterns) == 0 {
			continue
		}
		cs, err := precompileSet(rs, c, lists)
		if err != nil {
			return nil, fmt.Errorf("schematron %s: %w", rs.Source, err)
		}
		c.Sets = append(c.Sets, cs)
	}
	return c, nil
}

func precompileSet(rs RuleSet, c *Compiled, lists map[string]int) (CompiledSet, error) { //nolint:gocognit // one pass over the ISO constructs; tested against the reference implementation
	cs := CompiledSet{Source: rs.Source, Namespaces: rs.Namespaces, Lets: rs.Lets}
	switch strings.ToLower(rs.QueryBinding) {
	case "", "xslt", "xslt1", "xpath":
	default:
		return cs, fmt.Errorf("query binding %q is not supported; only XPath 1.0 (xslt) is", rs.QueryBinding)
	}
	patterns, err := instantiatePatterns(rs.Patterns)
	if err != nil {
		return cs, err
	}
	abstractRules := map[string]Rule{}
	for _, p := range patterns {
		for _, r := range p.Rules {
			if r.Abstract {
				if r.ID == "" {
					return cs, fmt.Errorf("abstract rule without id in pattern %q", p.ID)
				}
				abstractRules[r.ID] = r
			}
		}
	}

	check := checker{ns: rs.Namespaces}
	vars := map[string]bool{}
	if err := check.lets(rs.Lets, vars); err != nil {
		return cs, err
	}
	for _, p := range patterns {
		cp := CompiledPattern{ID: p.ID, Lets: p.Lets}
		pvars := maps.Clone(vars)
		if err := check.lets(p.Lets, pvars); err != nil {
			return cs, fmt.Errorf("pattern %q: %w", p.ID, err)
		}
		for _, r := range p.Rules {
			if r.Abstract {
				continue
			}
			r, err := extendRule(r, abstractRules, nil)
			if err != nil {
				return cs, fmt.Errorf("pattern %q: %w", p.ID, err)
			}
			cr, err := precompileRule(r, rs, pvars, check, c, lists)
			if err != nil {
				return cs, fmt.Errorf("pattern %q, rule %q: %w", p.ID, r.Context, err)
			}
			cp.Rules = append(cp.Rules, cr)
		}
		cs.Patterns = append(cs.Patterns, cp)
	}
	return cs, nil
}

func precompileRule(r Rule, rs RuleSet, patternVars map[string]bool, check checker, c *Compiled, lists map[string]int) (CompiledRule, error) {
	cr := CompiledRule{Context: contextToXPath(r.Context), Lets: r.Lets}
	if err := check.expr(cr.Context, patternVars); err != nil {
		return cr, err
	}
	vars := maps.Clone(patternVars)
	if err := check.lets(r.Lets, vars); err != nil {
		return cr, err
	}
	for _, a := range r.Assertions {
		ca := CompiledAssertion{ID: a.ID, Report: a.Report, Role: a.Role, Flag: a.Flag, Message: slices.Clone(a.Message)}
		if an := parseAttributeNameCheck(a.Test); an != nil {
			key := strings.Join(an.names, "\x00")
			idx, ok := lists[key]
			if !ok {
				idx = len(c.NameLists)
				lists[key] = idx
				c.NameLists = append(c.NameLists, an.names)
			}
			ca.AttributeName = &AttributeName{Position: an.position, Empty: an.empty, Names: idx, Prefixes: an.prefixes}
		} else {
			if err := check.expr(a.Test, vars); err != nil {
				return cr, err
			}
			ca.Test = a.Test
		}
		for _, id := range a.Diagnostics {
			d, ok := rs.Diagnostics[id]
			if !ok {
				return cr, fmt.Errorf("diagnostic %q not found", id)
			}
			ca.Message = append(append(ca.Message, MessagePart{Text: " "}), d...)
		}
		for _, part := range ca.Message {
			if part.Select != "" {
				if err := check.expr(part.Select, vars); err != nil {
					return cr, err
				}
			}
		}
		cr.Assertions = append(cr.Assertions, ca)
	}
	return cr, nil
}

// checker compiles expressions only to check them; the result is dropped.
type checker struct{ ns map[string]string }

func (k checker) expr(src string, vars map[string]bool) error {
	_, err := xpath.Compile(src, xpath.Options{Namespaces: k.ns, Variables: vars})
	return err
}

func (k checker) lets(lets []Let, vars map[string]bool) error {
	for _, l := range lets {
		if err := k.expr(l.Value, vars); err != nil {
			return fmt.Errorf("let $%s: %w", l.Name, err)
		}
		vars[l.Name] = true
	}
	return nil
}

// instantiatePatterns replaces every pattern with is-a by a copy of the
// abstract pattern with its parameters substituted, and drops the abstract
// patterns themselves. Substitution is textual, as ISO Schematron defines.
func instantiatePatterns(patterns []Pattern) ([]Pattern, error) {
	abstract := map[string]Pattern{}
	for _, p := range patterns {
		if p.Abstract {
			if p.ID == "" {
				return nil, errors.New("abstract pattern without id")
			}
			abstract[p.ID] = p
		}
	}
	var out []Pattern
	for _, p := range patterns {
		switch {
		case p.Abstract:
			continue
		case p.IsA == "":
			out = append(out, p)
			continue
		}
		base, ok := abstract[p.IsA]
		if !ok {
			return nil, fmt.Errorf("pattern %q: abstract pattern %q not found", p.ID, p.IsA)
		}
		sub := paramReplacer(p.Params)
		inst := Pattern{ID: p.ID, Lets: append(substituteLets(base.Lets, sub), p.Lets...)}
		for _, r := range base.Rules {
			r.Context = sub(r.Context)
			r.Lets = substituteLets(r.Lets, sub)
			r.Assertions = slices.Clone(r.Assertions)
			for i := range r.Assertions {
				a := &r.Assertions[i]
				a.Test = sub(a.Test)
				a.Message = slices.Clone(a.Message)
				for j := range a.Message {
					a.Message[j].Select = sub(a.Message[j].Select)
				}
			}
			inst.Rules = append(inst.Rules, r)
		}
		out = append(out, inst)
	}
	return out, nil
}

// paramReplacer substitutes $name for each parameter. A reference is the
// whole name, so $a does not match the start of $a-b.
func paramReplacer(params map[string]string) func(string) string {
	return func(s string) string {
		return paramRef.ReplaceAllStringFunc(s, func(m string) string {
			if v, ok := params[m[1:]]; ok {
				return v
			}
			return m
		})
	}
}

var paramRef = regexp.MustCompile(`\$[\pL_][\pL\pN_.\-]*`)

func substituteLets(lets []Let, sub func(string) string) []Let {
	out := make([]Let, len(lets))
	for i, l := range lets {
		out[i] = Let{Name: l.Name, Value: sub(l.Value)}
	}
	return out
}

// extendRule appends the lets and assertions of the abstract rules a rule
// extends, recursively.
func extendRule(r Rule, abstract map[string]Rule, seen []string) (Rule, error) {
	for _, id := range r.Extends {
		if slices.Contains(seen, id) {
			return r, fmt.Errorf("abstract rule %q extends itself", id)
		}
		base, ok := abstract[id]
		if !ok {
			return r, fmt.Errorf("abstract rule %q not found", id)
		}
		base, err := extendRule(base, abstract, append(seen, id))
		if err != nil {
			return r, err
		}
		r.Lets = append(slices.Clone(r.Lets), base.Lets...)
		r.Assertions = append(slices.Clone(r.Assertions), base.Assertions...)
	}
	r.Extends = nil
	return r, nil
}

// contextToXPath turns a rule context, an XSLT 1.0 pattern, into an XPath
// expression selecting the nodes it matches: each relative alternative
// matches anywhere in the document.
func contextToXPath(pattern string) string {
	alts := splitTopLevel(pattern, '|')
	for i, alt := range alts {
		alt = strings.TrimSpace(alt)
		if !strings.HasPrefix(alt, "/") && !strings.HasPrefix(alt, "id(") {
			alt = "//" + alt
		}
		alts[i] = alt
	}
	return strings.Join(alts, " | ")
}

// splitTopLevel splits s on sep outside brackets, parentheses and quotes.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	depth, start := 0, 0
	var quote byte
	for i := range len(s) {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '(' || c == '[':
			depth++
		case c == ')' || c == ']':
			depth--
		case c == sep && depth == 0:
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}
