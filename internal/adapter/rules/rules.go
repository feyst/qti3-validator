// Package rules runs Schematron rules with the engine in
// internal/lib/schematron, and implements app.RuleChecker.
package rules

import (
	"bytes"
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

// Checker runs one or more rule sets on a document, which it parses once.
// It is safe for concurrent use.
type Checker struct {
	sets []ruleSet
	// types runs the type checks of types.go after the rule sets.
	types bool
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

// CheckRules implements app.RuleChecker. Each rule set runs while fewer than
// maxFindings errors were found; warnings do not count. A nil Checker has no
// rules.
func (c *Checker) CheckRules(data []byte, maxFindings int) ([]app.RuleFinding, error) {
	if c == nil {
		return nil, nil
	}
	tree, err := xpath.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("parse for Schematron: " + err.Error())
	}
	var out []app.RuleFinding
	errorCount := 0
	for _, set := range c.sets {
		remaining := 0 // no limit
		if maxFindings > 0 {
			if remaining = maxFindings - errorCount; remaining <= 0 {
				break
			}
		}
		failures, err := set.engine.Validate(tree, remaining)
		if err != nil {
			return nil, errors.New("Schematron: " + err.Error())
		}
		for _, f := range failures {
			rf := finding(f, set.mounted)
			if !rf.Warning {
				errorCount++
			}
			out = append(out, rf)
		}
	}
	if c.types {
		for _, f := range checkTypes(tree) {
			if maxFindings > 0 && errorCount >= maxFindings {
				break
			}
			errorCount++
			out = append(out, f)
		}
	}
	return out, nil
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
