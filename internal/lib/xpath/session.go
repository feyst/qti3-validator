package xpath

import (
	"context"
	"errors"
)

// ErrTooComplex is returned when an evaluation needs more steps than its
// session's budget allows.
var ErrTooComplex = errors.New("evaluation exceeds its step budget")

// checkInterval is how many steps pass between checks of the context. A step
// takes tens of nanoseconds, so a cancellation is seen well within a
// millisecond, and the check itself costs nothing measurable.
const checkInterval = 1 << 12

// Session is what the evaluations over one document share: the context that
// cancels them, a budget of steps, and the node-sets of absolute paths that
// do not depend on their context, which are the same for every evaluation.
// It is not safe for concurrent use.
//
// A step is a node visited on an axis, a predicate evaluated, a pair of
// values compared, a reuse of a remembered node-set, or 256 bytes of the
// string-value of an element joined from the text below it. The budget counts
// work rather than time, so a document that is too expensive is stopped at
// the same point on every machine.
type Session struct {
	ctx        context.Context
	limited    bool
	remaining  int64
	steps      int64 // counted so far, with or without a budget
	untilCheck int
	memo       map[memoKey]NodeSet
	keys       map[memoKey]keyIndex
	names      map[*Node]nameIndex
	// sets holds node-sets that stay the same throughout the session: the
	// remembered node-sets and those that Constant was told of. Each maps
	// to the string-values of its nodes once a comparison needed them.
	sets map[setKey]*stringSet
	// setBytes is the number of bytes of string-values built for sets that
	// the session holds on to; see maxSetBytes.
	setBytes int
}

// setKey identifies a node-set by its first element in memory, which the
// key keeps alive, and its length.
//
// The identity holds only while no node-set is changed in place: a
// node-set sorted, reversed, filtered or appended to within its own array
// would keep its key and no longer have the string-values collected for it.
// The evaluator therefore never writes to a node-set it was given or has
// returned: a union copies its operands, filters build new sets, and the
// sets that are shared (remembered paths, index lists) have their capacity
// clipped so that appending copies. TestConstantNodeSetsAreNotChanged guards
// this for the values of variables.
type setKey struct {
	first **Node
	n     int
}

// stringSet is the set of the string-values of the nodes of a kept
// node-set.
type stringSet struct {
	values map[string]bool // nil until a comparison needs it
	bytes  int             // of string-values built for it; see maxSetBytes
	// unusable: the string-values take too much memory to keep; the
	// node-set is compared node by node.
	unusable bool
}

// minSetSize is the least size of a node-set whose string-values a session
// collects for comparisons. Smaller node-sets are compared node by node.
const minSetSize = 16

// maxSetBytes bounds the string-values a session builds and keeps for its
// sets. The string-value of an attribute or a text node is part of the
// document, but that of an element is built from the text of its
// descendants, and elements that nest each repeat the text of the
// innermost: without a bound, a set of n nested elements around a text
// would keep n copies of it for the whole session. A set that does not fit
// in what is left of the bound is dropped and compared node by node, which
// builds the same strings but keeps none.
const maxSetBytes = 4 << 20

// stringBytesPerStep is the number of bytes of a string-value built from
// the text below an element that cost a step.
const stringBytesPerStep = 256

// stringNodesPerStep is the number of nodes visited to join a string-value
// that cost a step.
const stringNodesPerStep = 64

// stringValue returns the string-value of n for an evaluation, and charges
// the session when it had to be built: a step per stringBytesPerStep bytes
// and per stringNodesPerStep nodes below n.
// A string-value that is memory of the document (see Node.stringValue)
// costs nothing beyond the step of whatever asked for it.
//
// Every string-value an evaluation takes of a node goes through here:
// comparisons, conversions to a string or a number, the arguments of the
// string functions, sum(), the keys of lookups and the result of
// EvaluateString. Without the charge, comparing elements that nest would
// copy their text once for each at a step each: the work of an evaluation
// would not show in its steps.
//
// The values are not kept. In the documents of the test data and the
// 1EdTech corpus the built-in rules never build one: every string-value
// they take is an attribute or the text of an element with one text node.
// A cache would hold nothing there, and rules that do build them are
// stopped by the charge.
func (s *Session) stringValue(n *Node) (string, error) {
	v, built := n.stringValue()
	if !built {
		return v, nil
	}
	// Joining the text visits every node below n, whatever text it finds.
	return v, s.charge(len(v)/stringBytesPerStep + int(n.end-n.order)/stringNodesPerStep)
}

// stringOf converts a value as string() does, through stringValue for a
// node-set.
func (s *Session) stringOf(v Value) (string, error) {
	if ns, ok := v.(NodeSet); ok {
		if len(ns) == 0 {
			return "", nil
		}
		return s.stringValue(ns[0])
	}
	return toString(v), nil
}

// numberOf converts a value as number() does, through stringValue for a
// node-set.
func (s *Session) numberOf(v Value) (float64, error) {
	if ns, ok := v.(NodeSet); ok {
		str, err := s.stringOf(ns)
		return stringToNumber(str), err
	}
	return toNumber(v), nil
}

// Constant tells s that v, the value of a variable, stays the same for
// every evaluation in it. Comparing a large node-set with = then looks the
// string-values of the other operand up instead of comparing every pair.
// Node-sets are never changed once evaluated (see setKey), so this holds
// for any value the caller keeps for the session and does not change
// itself.
func (s *Session) Constant(v Value) {
	if ns, ok := v.(NodeSet); ok {
		s.keep(ns)
	}
}

// Scoped tells s that v, the value of a variable, stays the same until the
// function it returns is called, as Constant does for the whole session:
// the value of a let of a rule, bound anew for every node the rule checks.
// Calling the function drops what the session collected for v, so that the
// values of every node do not add up.
func (s *Session) Scoped(v Value) (release func()) {
	ns, ok := v.(NodeSet)
	if !ok || len(ns) < minSetSize {
		return func() {}
	}
	k := setKey{&ns[0], len(ns)}
	if _, kept := s.sets[k]; kept {
		return func() {} // kept for the session: a remembered path or a constant
	}
	s.keep(ns)
	return func() {
		if set := s.sets[k]; set != nil {
			s.setBytes -= set.bytes
			delete(s.sets, k)
		}
	}
}

// stringInSet reports whether str, a string, equals the string-value of a
// node of ns, that is, whether ns = str, when ns is a kept node-set: one
// lookup, a step, where comparing every node takes a step each. It returns
// false for ok when op is not =, str is not a string, or ns was not kept.
func (s *Session) stringInSet(op string, ns NodeSet, str Value) (found, ok bool, err error) {
	text, isString := str.(string)
	if op != "=" || !isString {
		return false, false, nil
	}
	set, ok, err := s.stringSet(ns)
	if !ok || err != nil {
		return false, ok, err
	}
	return set[text], true, s.charge(1)
}

func (s *Session) keep(ns NodeSet) {
	if len(ns) < minSetSize {
		return
	}
	if s.sets == nil {
		s.sets = map[setKey]*stringSet{}
	}
	k := setKey{&ns[0], len(ns)}
	if _, ok := s.sets[k]; !ok {
		s.sets[k] = &stringSet{}
	}
}

// stringSet returns the string-values of the nodes of a kept node-set,
// collecting them the first time at a step per node and per
// stringBytesPerStep bytes built. It returns false when ns was not kept, or
// when its string-values do not fit in maxSetBytes.
func (s *Session) stringSet(ns NodeSet) (map[string]bool, bool, error) {
	if len(ns) < minSetSize || !optimise {
		return nil, false, nil
	}
	set, ok := s.sets[setKey{&ns[0], len(ns)}]
	if !ok || set.unusable {
		return nil, false, nil
	}
	if set.values != nil {
		return set.values, true, nil
	}
	if err := s.charge(len(ns)); err != nil {
		return nil, true, err
	}
	values := make(map[string]bool, len(ns))
	built := 0
	for _, n := range ns {
		v, wasBuilt := n.stringValue()
		if wasBuilt {
			// Built for this node; other string-values are the document's.
			built += len(v)
			if err := s.charge(len(v)/stringBytesPerStep + int(n.end-n.order)/stringNodesPerStep); err != nil {
				return nil, true, err
			}
			if s.setBytes+built > maxSetBytes {
				set.unusable = true
				return nil, false, nil
			}
		}
		values[v] = true
	}
	s.setBytes += built
	set.values, set.bytes = values, built
	return values, true, nil
}

// anyInSet reports whether a node of a has the string-value of a node of b,
// that is, whether a = b, when either is a kept node-set: it then looks the
// string-values of the other up, a step each. It returns false for ok when
// neither was kept.
func (s *Session) anyInSet(a, b NodeSet) (found, ok bool, err error) {
	set, ok, err := s.stringSet(b)
	if !ok {
		if set, ok, err = s.stringSet(a); !ok {
			return false, false, nil
		}
		a = b
	}
	if err != nil {
		return false, true, err
	}
	if err := s.charge(len(a)); err != nil {
		return false, true, err
	}
	for _, n := range a {
		v, err := s.stringValue(n)
		if err != nil {
			return false, true, err
		}
		if set[v] {
			return true, true, nil
		}
	}
	return false, true, nil
}

type memoKey struct {
	path *pathExpr
	root *Node
}

// NewSession returns a session that stops evaluations with ctx.Err() when
// ctx is done, and with ErrTooComplex after budget steps; a budget of zero or
// less sets no limit.
func NewSession(ctx context.Context, budget int64) *Session {
	// The first step checks the context.
	return &Session{ctx: ctx, limited: budget > 0, remaining: budget}
}

// Charge counts n steps of work that a caller does for the session outside
// the evaluation of an expression, such as sorting the nodes it selected.
// It fails as an evaluation does, with ErrTooComplex or the error of the
// session's context.
func (s *Session) Charge(n int) error { return s.charge(n) }

// Steps returns the number of steps the session has counted so far: the
// work of its evaluations, the same on every machine.
func (s *Session) Steps() int64 { return s.steps }

// charge counts n steps.
func (s *Session) charge(n int) error {
	s.steps += int64(n)
	if s.limited {
		if s.remaining -= int64(n); s.remaining < 0 {
			return ErrTooComplex
		}
	}
	if s.untilCheck -= n; s.untilCheck <= 0 {
		s.untilCheck = checkInterval
		return s.ctx.Err()
	}
	return nil
}

// remember returns the node-set of an independent path in the document of
// root, evaluating it the first time. The node-set is shared, so its
// capacity is clipped: appending to it copies.
func (s *Session) remember(p *pathExpr, root *Node, eval func() (NodeSet, error)) (NodeSet, error) {
	k := memoKey{path: p, root: root}
	if ns, ok := s.memo[k]; ok {
		return ns, s.charge(1)
	}
	ns, err := eval()
	if err != nil {
		return nil, err
	}
	ns = ns[:len(ns):len(ns)]
	s.keep(ns)
	if s.memo == nil {
		s.memo = map[memoKey]NodeSet{}
	}
	s.memo[k] = ns
	return ns, nil
}

// keyIndex returns the index of a keyed path in the document of root,
// building it the first time.
func (s *Session) keyIndex(p *pathExpr, root *Node, build func() (keyIndex, error)) (keyIndex, error) {
	k := memoKey{path: p, root: root}
	if idx, ok := s.keys[k]; ok {
		return idx, s.charge(1)
	}
	idx, err := build()
	if err != nil {
		return keyIndex{}, err
	}
	if s.keys == nil {
		s.keys = map[memoKey]keyIndex{}
	}
	s.keys[k] = idx
	return idx, nil
}
