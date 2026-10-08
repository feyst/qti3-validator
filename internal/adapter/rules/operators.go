package rules

import (
	"strings"

	"qti3-validator/internal/domain/qti"
	"qti3-validator/internal/lib/xpath"
)

// operandBase is what base type the operands of an operator must have.
type operandBase int

const (
	anyBase operandBase = iota
	numeric             // integer or float
	integerOnly
	booleanOnly
	stringOnly
	pointOnly
	durationOnly
)

func (b operandBase) accepts(t qti.BaseType) bool {
	switch b {
	case numeric:
		return t.IsNumeric()
	case integerOnly:
		return t == qti.Integer
	case booleanOnly:
		return t == qti.Boolean
	case stringOnly:
		return t == qti.String
	case pointOnly:
		return t == qti.Point
	case durationOnly:
		return t == qti.Duration
	}
	return true
}

func (b operandBase) String() string {
	return [...]string{"any", "numerical", "integer", "boolean", "string", "point", "duration"}[b]
}

// operandCard is what cardinality the operands of an operator must have.
type operandCard int

const (
	anyCard operandCard = iota
	singleOnly
	containerOnly // multiple or ordered
)

func (c operandCard) accepts(t qti.Cardinality) bool {
	switch c {
	case singleOnly:
		return t == qti.Single
	case containerOnly:
		return t.IsContainer()
	}
	return true
}

// operator is how an expression element is typed. The rules come from the
// description of each expression in the QTI 3 information model, section
// 2.11, for example "The qti-sum operator takes 1 or more sub-expressions
// which all have numerical base-types and may have single, multiple or
// ordered cardinality."
type operator struct {
	base     operandBase
	card     operandCard
	sameBase bool // all operands must have the same base type
	sameCard bool // all operands must have the same cardinality
	result   func(c *typeChecker, n *xpath.Node, ops []valueType) valueType
	// check, if set, checks operands that need more than the above.
	check func(c *typeChecker, n *xpath.Node, ops []valueType, nodes []*xpath.Node)
}

func fixed(b qti.BaseType, card qti.Cardinality) func(*typeChecker, *xpath.Node, []valueType) valueType {
	return func(*typeChecker, *xpath.Node, []valueType) valueType { return valueType{b, card} }
}

var (
	booleanResult = fixed(qti.Boolean, qti.Single)
	integerResult = fixed(qti.Integer, qti.Single)
	floatResult   = fixed(qti.Float, qti.Single)
	unknownResult = fixed("", "")
)

// numericResult is integer when every operand is, float when one is.
func numericResult(_ *typeChecker, _ *xpath.Node, ops []valueType) valueType {
	b := qti.Integer
	for _, o := range ops {
		switch o.base {
		case qti.Float:
			return valueType{qti.Float, qti.Single}
		case qti.Integer:
		default:
			b = ""
		}
	}
	return valueType{b, qti.Single}
}

// commonBase is the operands' base type when they agree on one.
func commonBase(ops []valueType) qti.BaseType {
	var b qti.BaseType
	for _, o := range ops {
		if o.base == "" || b != "" && o.base != b {
			return ""
		}
		b = o.base
	}
	return b
}

func container(card qti.Cardinality) func(*typeChecker, *xpath.Node, []valueType) valueType {
	return func(_ *typeChecker, _ *xpath.Node, ops []valueType) valueType {
		return valueType{commonBase(ops), card}
	}
}

func firstBase(card qti.Cardinality) func(*typeChecker, *xpath.Node, []valueType) valueType {
	return func(_ *typeChecker, _ *xpath.Node, ops []valueType) valueType {
		if len(ops) == 0 {
			return valueType{"", card}
		}
		return valueType{ops[0].base, card}
	}
}

func variableResult(c *typeChecker, n *xpath.Node, _ []valueType) valueType {
	return c.vars[attr(n, "identifier")] // unknown when not declared here, as ITEM.VAR in a test
}

// singleThenContainer checks qti-member and qti-delete: a single value and a
// container of the same base type.
func singleThenContainer(c *typeChecker, n *xpath.Node, ops []valueType, nodes []*xpath.Node) {
	if len(ops) != 2 {
		return
	}
	if ops[0].card != "" && ops[0].card != qti.Single {
		c.report(nodes[0], CheckExpressionType, "%s needs a single value first, but %s gives %s.", n.Local, nodes[0].Local, ops[0])
	}
	if ops[1].card != "" && !ops[1].card.IsContainer() {
		c.report(nodes[1], CheckExpressionType, "%s needs a multiple or ordered value second, but %s gives %s.", n.Local, nodes[1].Local, ops[1])
	}
}

var operators map[string]operator

// init fills the table; it refers to functions that use the table.
func init() {
	operators = map[string]operator{
		"qti-base-value": {result: func(_ *typeChecker, n *xpath.Node, _ []valueType) valueType {
			return valueType{qti.BaseType(attr(n, "base-type")), qti.Single}
		}},
		"qti-variable":           {result: variableResult},
		"qti-default":            {result: variableResult},
		"qti-correct":            {result: variableResult},
		"qti-map-response":       {result: floatResult},
		"qti-map-response-point": {result: floatResult},
		"qti-math-constant":      {result: floatResult},
		"qti-null":               {result: unknownResult},
		"qti-field-value":        {result: unknownResult},
		"qti-custom-operator":    {result: unknownResult},
		"qti-random-integer":     {result: integerResult},
		"qti-random-float":       {result: floatResult},
		"qti-number-correct":     {result: integerResult},
		"qti-number-incorrect":   {result: integerResult},
		"qti-number-presented":   {result: integerResult},
		"qti-number-responded":   {result: integerResult},
		"qti-number-selected":    {result: integerResult},
		"qti-test-variables":     {result: fixed("", qti.Multiple)},
		"qti-outcome-maximum":    {result: fixed(qti.Float, qti.Multiple)},
		"qti-outcome-minimum":    {result: fixed(qti.Float, qti.Multiple)},

		"qti-multiple":       {sameBase: true, result: container(qti.Multiple)},
		"qti-ordered":        {sameBase: true, result: container(qti.Ordered)},
		"qti-container-size": {card: containerOnly, result: integerResult},
		"qti-is-null":        {result: booleanResult},
		"qti-index":          {card: containerOnly, result: firstBase(qti.Single)},
		"qti-random":         {card: containerOnly, result: firstBase(qti.Single)},
		"qti-member":         {sameBase: true, result: booleanResult, check: singleThenContainer},
		"qti-delete": {sameBase: true, check: singleThenContainer, result: func(_ *typeChecker, _ *xpath.Node, ops []valueType) valueType {
			if len(ops) != 2 {
				return valueType{}
			}
			return valueType{ops[0].base, ops[1].card}
		}},
		"qti-contains":  {card: containerOnly, sameBase: true, sameCard: true, result: booleanResult},
		"qti-substring": {base: stringOnly, card: singleOnly, result: booleanResult},
		"qti-repeat":    {sameBase: true, result: container(qti.Ordered)},

		"qti-not":           {base: booleanOnly, card: singleOnly, result: booleanResult},
		"qti-and":           {base: booleanOnly, card: singleOnly, result: booleanResult},
		"qti-or":            {base: booleanOnly, card: singleOnly, result: booleanResult},
		"qti-any-n":         {base: booleanOnly, card: singleOnly, result: booleanResult},
		"qti-match":         {sameBase: true, sameCard: true, result: booleanResult},
		"qti-string-match":  {base: stringOnly, card: singleOnly, result: booleanResult},
		"qti-pattern-match": {base: stringOnly, card: singleOnly, result: booleanResult},
		"qti-equal":         {base: numeric, card: singleOnly, result: booleanResult},
		"qti-equal-rounded": {base: numeric, card: singleOnly, result: booleanResult},
		"qti-lt":            {base: numeric, card: singleOnly, result: booleanResult},
		"qti-gt":            {base: numeric, card: singleOnly, result: booleanResult},
		"qti-lte":           {base: numeric, card: singleOnly, result: booleanResult},
		"qti-gte":           {base: numeric, card: singleOnly, result: booleanResult},
		"qti-duration-lt":   {base: durationOnly, card: singleOnly, result: booleanResult},
		"qti-duration-gte":  {base: durationOnly, card: singleOnly, result: booleanResult},
		"qti-inside":        {base: pointOnly, result: booleanResult},

		"qti-sum":              {base: numeric, result: numericResult},
		"qti-product":          {base: numeric, result: numericResult},
		"qti-max":              {base: numeric, result: numericResult},
		"qti-min":              {base: numeric, result: numericResult},
		"qti-subtract":         {base: numeric, card: singleOnly, result: numericResult},
		"qti-divide":           {base: numeric, card: singleOnly, result: floatResult},
		"qti-power":            {base: numeric, card: singleOnly, result: floatResult},
		"qti-round-to":         {base: numeric, card: singleOnly, result: floatResult},
		"qti-math-operator":    {base: numeric, card: singleOnly, result: floatResult},
		"qti-truncate":         {base: numeric, card: singleOnly, result: integerResult},
		"qti-round":            {base: numeric, card: singleOnly, result: integerResult},
		"qti-integer-divide":   {base: integerOnly, card: singleOnly, result: integerResult},
		"qti-integer-modulus":  {base: integerOnly, card: singleOnly, result: integerResult},
		"qti-integer-to-float": {base: integerOnly, card: singleOnly, result: floatResult},
		"qti-gcd":              {base: integerOnly, result: integerResult},
		"qti-lcm":              {base: integerOnly, result: integerResult},
		"qti-stats-operator":   {base: numeric, card: containerOnly, result: floatResult},
	}
}

// infer returns the type of an expression and reports operands of the wrong
// type, in it and in its sub-expressions. Operands of an unknown type are
// never reported.
func (c *typeChecker) infer(n *xpath.Node) valueType {
	op, known := operators[n.Local]
	nodes := elements(n)
	ops := make([]valueType, len(nodes))
	for i, child := range nodes {
		ops[i] = c.infer(child)
	}
	if n.Local == "qti-base-value" {
		if b := qti.BaseType(attr(n, "base-type")); !qti.ValidValue(b, n.StringValue()) {
			c.report(n, CheckValueType, "qti-base-value: %q is not a valid %s value.", strings.TrimSpace(n.StringValue()), b)
		}
	}
	if !known {
		return valueType{}
	}
	for i, o := range ops {
		if o.base != "" && !op.base.accepts(o.base) {
			c.report(nodes[i], CheckExpressionType, "%s needs %s values, but %s gives %s.", n.Local, op.base, nodes[i].Local, o)
		} else if o.card != "" && !op.card.accepts(o.card) {
			c.report(nodes[i], CheckExpressionType, "%s needs %s values, but %s gives %s.", n.Local, cardName(op.card), nodes[i].Local, o)
		}
	}
	if op.sameBase || op.sameCard {
		for i := 1; i < len(ops); i++ {
			a, b := ops[0], ops[i]
			if op.sameBase && a.base != "" && b.base != "" && a.base != b.base ||
				op.sameCard && a.card != "" && b.card != "" && a.card != b.card {
				c.report(nodes[i], CheckExpressionType, "%s needs operands of the same type, but %s gives %s and %s gives %s.",
					n.Local, nodes[0].Local, a, nodes[i].Local, b)
			}
		}
	}
	if op.check != nil {
		op.check(c, n, ops, nodes)
	}
	return op.result(c, n, ops)
}

func cardName(c operandCard) string {
	return [...]string{"any", "single", "multiple or ordered"}[c]
}

// elements returns the element children of n.
func elements(n *xpath.Node) []*xpath.Node {
	var out []*xpath.Node
	for _, c := range n.Children {
		if c.Kind == xpath.ElementNode {
			out = append(out, c)
		}
	}
	return out
}

// firstElement returns the first element child of n, or nil.
func firstElement(n *xpath.Node) *xpath.Node {
	for _, c := range n.Children {
		if c.Kind == xpath.ElementNode {
			return c
		}
	}
	return nil
}

func attr(n *xpath.Node, name string) string {
	for _, a := range n.Attrs {
		if a.Space == "" && a.Local == name {
			return a.Data
		}
	}
	return ""
}
