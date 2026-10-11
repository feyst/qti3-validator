// Package rules runs Schematron rules with the engine in
// internal/lib/schematron, and implements app.RuleChecker.
package rules

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"qti3-validator/internal/app"
	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/schematron"
	"qti3-validator/internal/lib/xpath"
)

// AdditionalChecks is the source name of the validator's own rules,
// rules/qti3-additional-checks.sch, as cmd/fetchschemas compiles them. They
// run on every QTI document after the rules embedded in the XSDs;
// docs/additional-checks.md describes them.
const AdditionalChecks = "qti3-additional-checks.sch"

// The rules may take BaseSteps plus StepsPerNode for every node of a
// document (xpath.Document.Nodes) in XPath steps (see xpath.Session). The
// work of rules grows with the document: every rule whose context is not a
// plain element name looks at every node, and every assertion runs once per
// context node. So the budget does too, and only work that grows faster than
// the document runs out of it.
//
// Measured with the built-in rules: the 1148 QTI documents of testdata,
// examples and the 1EdTech corpus take at most 209,000 steps and at most 222
// steps a node; plain content at the 10 MiB of MAX_REQUEST_SIZE — some 2
// million nodes of empty paragraphs, 1.3 million of p, div and span, 1.6
// million of table cells, 1.1 million of images, 1.2 million of attributes,
// half a million of choices or MathML — takes 49 to 155 steps a node, and
// 5,000 nested divisions 170. StepsPerNode leaves 4.5 times the worst of
// these; BaseSteps keeps the budget of a small document what it was before
// the budget grew with the document, so no document that passed then runs
// out now. Work that grows faster than the document still runs out: a rule
// that compares every pair of 20,000 choices takes 800 million steps on a
// budget of 160 million, and one that compares every declaration with every
// interaction at each of 400 interactions, unable to remember its paths,
// more than a billion on a budget of 104 million.
//
// At 30 to 50 ns a step, BaseSteps is some 3 to 5 seconds of work. A
// document at MAX_REQUEST_SIZE gets a budget of some 2 billion steps,
// about as much work as the default REQUEST_TIMEOUT allows or more: for
// large documents that timeout, not the budget, is what bounds the work.
const (
	BaseSteps    = 100_000_000
	StepsPerNode = 1_000
)

// budget is how many steps the rules may take on a document of n nodes.
type budget struct{ base, perNode int64 }

func (b budget) steps(n int) int64 { return b.base + b.perNode*int64(n) }

// Checker runs one or more rule sets on a document, which it parses once.
// It is safe for concurrent use.
type Checker struct {
	sets []ruleSet
	// types runs the type checks of types.go after the rule sets.
	types bool
	// budget replaces BaseSteps and StepsPerNode when it is not zero.
	budget budget
}

var _ app.RuleChecker = (*Checker)(nil)

type ruleSet struct {
	engine *schematron.Engine
	// mounted rules come from the validators directory; their findings name
	// their file in Source.
	mounted bool
}

// ForVersion returns the built-in rules of a QTI version: those embedded in
// its XSDs and in the LOM schema, the additional checks, and the type checks
// of types.go.
func ForVersion(compiled *schematron.Compiled, v qti.Version) (*Checker, error) {
	subset, err := compiled.Subset(v.ASI, qti.LOMSchema, AdditionalChecks)
	if err != nil {
		return nil, fmt.Errorf("QTI %s: %w", v.Name, err)
	}
	engine, err := schematron.New(subset)
	if err != nil {
		return nil, fmt.Errorf("QTI %s: %w", v.Name, err)
	}
	return &Checker{sets: []ruleSet{{engine: engine}}, types: true}, nil
}

// Mounted returns a checker for rules from the validators directory.
func Mounted(engine *schematron.Engine) *Checker {
	return &Checker{sets: []ruleSet{{engine: engine, mounted: true}}}
}

// Chain returns a checker that runs the given checkers in order. Nil
// checkers are skipped; it returns nil when all are.
func Chain(checkers ...*Checker) *Checker {
	var sets []ruleSet
	types := false
	for _, c := range checkers {
		if c != nil {
			sets = append(sets, c.sets...)
			types = types || c.types
		}
	}
	if len(sets) == 0 && !types {
		return nil
	}
	return &Checker{sets: sets, types: types}
}

// CheckRules implements app.RuleChecker. The rule sets and the type checks
// run in order until limits.Errors errors were found; warnings do not count
// towards that, and only the first limits.Warnings of them are kept; when
// more were found, a last warning with code warnings_omitted says how many. A
// nil Checker has no rules.
//
// All rule sets share one budget of XPath steps for the document:
// BaseSteps plus StepsPerNode for every node. When it runs out, or ctx is
// done, CheckRules returns the findings so far with an error wrapping
// app.ErrTooComplex or ctx.Err(). When a rule fails, it returns the findings
// so far with an error that wraps neither.
func (c *Checker) CheckRules(ctx context.Context, data []byte, limits app.RuleLimits) ([]app.RuleFinding, error) {
	if c == nil {
		return nil, nil
	}
	tree, err := xpath.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("parse for Schematron: " + err.Error())
	}
	b := c.budget
	if b == (budget{}) {
		b = budget{base: BaseSteps, perNode: StepsPerNode}
	}
	session := xpath.NewSession(ctx, b.steps(tree.Nodes))
	var out []app.RuleFinding
	errorCount, warningCount, omitted := 0, 0, 0
	for _, set := range c.sets {
		room, ok := roomLeft(limits, errorCount, warningCount)
		if !ok {
			break
		}
		res, err := set.engine.Check(session, tree, room)
		omitted += res.OmittedWarnings
		for _, f := range res.Failures {
			rf := finding(f, set.mounted)
			if rf.Warning {
				warningCount++
			} else {
				errorCount++
			}
			out = append(out, rf)
		}
		switch {
		case errors.Is(err, xpath.ErrTooComplex):
			return withOmitted(out, omitted, limits), fmt.Errorf("%w: %w", app.ErrTooComplex, err)
		case ctx.Err() != nil && errors.Is(err, ctx.Err()):
			return withOmitted(out, omitted, limits), err
		case err != nil:
			return withOmitted(out, omitted, limits), errors.New("Schematron: " + err.Error())
		}
	}
	if c.types {
		for _, f := range checkTypes(tree) { // errors only
			if limits.Errors > 0 && errorCount >= limits.Errors {
				break
			}
			errorCount++
			out = append(out, f)
		}
	}
	return withOmitted(out, omitted, limits), nil
}

// withOmitted adds a note to findings when warnings were left out, so that
// the cut shows: one warning, on top of limits.Warnings, that says how many.
// It counts the warnings found until the rules ended; rules that stop
// early, at limits.Errors or with an error, do not look for more.
func withOmitted(findings []app.RuleFinding, omitted int, limits app.RuleLimits) []app.RuleFinding {
	if omitted == 0 {
		return findings
	}
	msg := fmt.Sprintf("%d further warnings of the rules were left out: only the first %d are reported (MAX_ERRORS).", omitted, limits.Warnings)
	if omitted == 1 {
		msg = fmt.Sprintf("1 further warning of the rules was left out: only the first %d are reported (MAX_ERRORS).", limits.Warnings)
	}
	return append(findings, app.RuleFinding{Warning: true, Finding: qti.Finding{Code: qti.CodeWarningsOmitted, Message: msg}})
}

// roomLeft returns the limits for the next rule set after the errors and
// warnings found so far, or false when there is no room for errors left.
func roomLeft(limits app.RuleLimits, errorCount, warningCount int) (schematron.Limits, bool) {
	var room schematron.Limits // zero: no limit
	if limits.Errors > 0 {
		if room.Errors = limits.Errors - errorCount; room.Errors <= 0 {
			return room, false
		}
	}
	if limits.Warnings > 0 {
		if room.Warnings = limits.Warnings - warningCount; room.Warnings <= 0 {
			room.Warnings = -1 // keep none
		}
	}
	return room, true
}

func finding(f schematron.Failure, mounted bool) app.RuleFinding {
	e := qti.Finding{
		Code:    qti.CodeSchematron,
		Rule:    f.Pattern,
		Line:    f.Node.Line,
		Column:  f.Node.Column,
		Path:    f.Node.Path(),
		Message: f.Message,
	}
	if f.Assertion != "" {
		e.Rule += "/" + f.Assertion
	}
	switch {
	case mounted:
		e.Source = f.Source
	case f.Source == AdditionalChecks:
		// Tells the validator's own rules apart from 1EdTech's in the
		// report's generator.
		e.Rule = AdditionalChecks + "#" + e.Rule
	}
	return app.RuleFinding{Finding: e, Warning: f.Warning}
}
