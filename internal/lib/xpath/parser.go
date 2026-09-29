package xpath

import (
	"fmt"
	"strings"
)

// Expr is a compiled XPath expression. It is immutable and safe for
// concurrent use.
type Expr struct {
	source string
	root   expr
}

// String returns the source of the expression.
func (e *Expr) String() string { return e.source }

// Options control compilation.
type Options struct {
	// Namespaces binds the prefixes the expression may use.
	Namespaces map[string]string
	// Variables lists the variables the expression may reference.
	Variables map[string]bool
}

// Compile parses an XPath 1.0 expression. Prefixes, variables, function
// names and arities are checked here, so evaluation fails only on dynamic
// type errors.
func Compile(source string, opts Options) (*Expr, error) {
	toks, err := lex(source)
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", source, err)
	}
	p := &parser{toks: toks, opts: opts}
	root, err := p.parseExpr()
	if err == nil && p.peek().kind != tokEOF {
		err = p.errorf("unexpected %q", p.peek().text)
	}
	if err != nil {
		return nil, fmt.Errorf("xpath %q: %w", source, err)
	}
	return &Expr{source: source, root: root}, nil
}

// mergeDescendantSteps rewrites descendant-or-self::node()/child::T into
// descendant::T, which selects the same nodes without building the
// intermediate set of every node in the subtree. It applies only when the
// child step has no predicates: //p[1] is not descendant::p[1].
func mergeDescendantSteps(steps []step) []step {
	out := steps[:0:0]
	for i := 0; i < len(steps); i++ {
		s := steps[i]
		if s.axis == axisDescendantOrSelf && s.test.kind == testNode && len(s.preds) == 0 &&
			i+1 < len(steps) && steps[i+1].axis == axisChild && len(steps[i+1].preds) == 0 {
			next := steps[i+1]
			next.axis = axisDescendant
			out = append(out, next)
			i++
			continue
		}
		out = append(out, s)
	}
	return out
}

type parser struct {
	toks []token
	i    int
	opts Options
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }

func (p *parser) errorf(format string, args ...any) error {
	return fmt.Errorf("at %d: %s", p.peek().pos, fmt.Sprintf(format, args...))
}

func (p *parser) isOp(ops ...string) (string, bool) {
	t := p.peek()
	if t.kind != tokOperator {
		return "", false
	}
	for _, op := range ops {
		if t.text == op {
			return op, true
		}
	}
	return "", false
}

func (p *parser) isPunct(s string) bool {
	t := p.peek()
	return t.kind == tokPunct && t.text == s
}

func (p *parser) expectPunct(s string) error {
	if !p.isPunct(s) {
		return p.errorf("expected %q", s)
	}
	p.i++
	return nil
}

func (p *parser) parseExpr() (expr, error) { return p.parseBinary(0) }

// Binary operators by precedence level, lowest first.
var binaryLevels = [][]string{
	{"or"},
	{"and"},
	{"=", "!="},
	{"<", "<=", ">", ">="},
	{"+", "-"},
	{"*", "div", "mod"},
}

func (p *parser) parseBinary(level int) (expr, error) {
	if level == len(binaryLevels) {
		return p.parseUnary()
	}
	left, err := p.parseBinary(level + 1)
	if err != nil {
		return nil, err
	}
	for {
		op, ok := p.isOp(binaryLevels[level]...)
		if !ok {
			return left, nil
		}
		p.i++
		right, err := p.parseBinary(level + 1)
		if err != nil {
			return nil, err
		}
		left = &binaryExpr{op: op, left: left, right: right}
	}
}

func (p *parser) parseUnary() (expr, error) {
	if _, ok := p.isOp("-"); ok {
		p.i++
		e, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &negateExpr{e: e}, nil
	}
	left, err := p.parsePath()
	if err != nil {
		return nil, err
	}
	for {
		if _, ok := p.isOp("|"); !ok {
			return left, nil
		}
		p.i++
		right, err := p.parsePath()
		if err != nil {
			return nil, err
		}
		left = &unionExpr{left: left, right: right}
	}
}

// parsePath parses a PathExpr: a location path, or a filter expression
// optionally followed by a relative location path.
func (p *parser) parsePath() (expr, error) {
	t := p.peek()
	switch {
	case t.kind == tokOperator && (t.text == "/" || t.text == "//"):
		p.i++
		path := &pathExpr{absolute: true}
		if t.text == "//" {
			path.steps = append(path.steps, descendantOrSelfStep())
		} else if !p.startsStep() {
			return path, nil // "/" alone selects the root
		}
		return path, p.parseRelativePath(path)
	case p.startsStep():
		path := &pathExpr{}
		return path, p.parseRelativePath(path)
	}
	filter, err := p.parseFilter()
	if err != nil {
		return nil, err
	}
	op, ok := p.isOp("/", "//")
	if !ok {
		return filter, nil
	}
	p.i++
	path := &pathExpr{filter: filter}
	if op == "//" {
		path.steps = append(path.steps, descendantOrSelfStep())
	}
	return path, p.parseRelativePath(path)
}

func (p *parser) startsStep() bool {
	t := p.peek()
	switch t.kind {
	case tokAxis, tokNameTest, tokNodeType:
		return true
	case tokPunct:
		return t.text == "." || t.text == ".." || t.text == "@"
	}
	return false
}

func (p *parser) parseRelativePath(path *pathExpr) error {
	for {
		s, err := p.parseStep()
		if err != nil {
			return err
		}
		path.steps = append(path.steps, s)
		op, ok := p.isOp("/", "//")
		if !ok {
			path.steps = mergeDescendantSteps(path.steps)
			return nil
		}
		p.i++
		if op == "//" {
			path.steps = append(path.steps, descendantOrSelfStep())
		}
	}
}

func descendantOrSelfStep() step {
	return step{axis: axisDescendantOrSelf, test: nodeTest{kind: testNode}}
}

func (p *parser) parseStep() (step, error) {
	if p.isPunct(".") {
		p.i++
		return step{axis: axisSelf, test: nodeTest{kind: testNode}}, nil
	}
	if p.isPunct("..") {
		p.i++
		return step{axis: axisParent, test: nodeTest{kind: testNode}}, nil
	}
	s := step{axis: axisChild}
	switch t := p.peek(); {
	case t.kind == tokPunct && t.text == "@":
		p.i++
		s.axis = axisAttribute
	case t.kind == tokAxis:
		p.i++
		a, ok := axisNames[t.text]
		if !ok {
			return s, p.errorf("unknown axis %q", t.text)
		}
		s.axis = a
		if err := p.expectPunct("::"); err != nil {
			return s, err
		}
	}
	test, err := p.parseNodeTest(s.axis)
	if err != nil {
		return s, err
	}
	s.test = test
	s.preds, err = p.parsePredicates()
	return s, err
}

func (p *parser) parseNodeTest(a axis) (nodeTest, error) {
	t := p.next()
	switch t.kind {
	case tokNodeType:
		if err := p.expectPunct("("); err != nil {
			return nodeTest{}, err
		}
		nt := nodeTest{}
		switch t.text {
		case "node":
			nt.kind = testNode
		case "text":
			nt.kind = testText
		case "comment":
			nt.kind = testComment
		case "processing-instruction":
			nt.kind = testPI
			if p.peek().kind == tokLiteral {
				nt.local = p.next().text
				nt.hasLocal = true
			}
		}
		return nt, p.expectPunct(")")
	case tokNameTest:
		nt := nodeTest{kind: testName, principal: principalKind(a)}
		switch {
		case t.text == "*":
			nt.anyLocal = true
		case strings.HasSuffix(t.text, ":*"):
			uri, err := p.namespace(strings.TrimSuffix(t.text, ":*"))
			if err != nil {
				return nt, err
			}
			nt.anyLocal, nt.space, nt.hasSpace = true, uri, true
		default:
			prefix, local, ok := strings.Cut(t.text, ":")
			if !ok {
				prefix, local = "", t.text
			}
			if prefix != "" {
				uri, err := p.namespace(prefix)
				if err != nil {
					return nt, err
				}
				nt.space, nt.hasSpace = uri, true
			}
			nt.local = local
		}
		return nt, nil
	}
	p.i--
	return nodeTest{}, p.errorf("expected a node test")
}

func (p *parser) namespace(prefix string) (string, error) {
	if prefix == "xml" {
		return NamespaceXML, nil
	}
	uri, ok := p.opts.Namespaces[prefix]
	if !ok {
		return "", p.errorf("undeclared namespace prefix %q", prefix)
	}
	return uri, nil
}

func (p *parser) parsePredicates() ([]expr, error) {
	var preds []expr
	for p.isPunct("[") {
		p.i++
		e, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if err := p.expectPunct("]"); err != nil {
			return nil, err
		}
		preds = append(preds, e)
	}
	return preds, nil
}

func (p *parser) parseFilter() (expr, error) {
	primary, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	preds, err := p.parsePredicates()
	if err != nil || len(preds) == 0 {
		return primary, err
	}
	return &filterExpr{primary: primary, preds: preds}, nil
}

func (p *parser) parsePrimary() (expr, error) {
	t := p.next()
	switch t.kind {
	case tokLiteral:
		return literalExpr{s: t.text}, nil
	case tokNumber:
		return numberExpr{n: t.num}, nil
	case tokVariable:
		if !p.opts.Variables[t.text] {
			p.i--
			return nil, p.errorf("undeclared variable $%s", t.text)
		}
		return variableExpr{name: t.text}, nil
	case tokPunct:
		if t.text == "(" {
			e, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			return e, p.expectPunct(")")
		}
	case tokFunction:
		return p.parseCall(t)
	}
	p.i--
	return nil, p.errorf("unexpected %q", t.text)
}

func (p *parser) parseCall(t token) (expr, error) {
	if err := p.expectPunct("("); err != nil {
		return nil, err
	}
	var args []expr
	if !p.isPunct(")") {
		for {
			a, err := p.parseExpr()
			if err != nil {
				return nil, err
			}
			args = append(args, a)
			if !p.isPunct(",") {
				break
			}
			p.i++
		}
	}
	if err := p.expectPunct(")"); err != nil {
		return nil, err
	}
	fn, ok := functions[t.text]
	if !ok {
		if unsupportedFunctions[t.text] {
			return nil, fmt.Errorf("function %s() is not supported", t.text)
		}
		return nil, fmt.Errorf("unknown function %s()", t.text)
	}
	if len(args) < fn.minArgs || fn.maxArgs >= 0 && len(args) > fn.maxArgs {
		return nil, fmt.Errorf("%s() called with %d arguments", t.text, len(args))
	}
	return &callExpr{name: t.text, fn: fn, args: args}, nil
}
