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
// values compared, or a reuse of a remembered node-set. The budget counts
// work rather than time, so a document that is too expensive is stopped at
// the same point on every machine.
type Session struct {
	ctx        context.Context
	limited    bool
	remaining  int64
	untilCheck int
	memo       map[memoKey]NodeSet
	keys       map[memoKey]keyIndex
	names      map[*Node]nameIndex
	// sets holds node-sets that stay the same throughout the session: the
	// remembered node-sets and those that Constant was told of. Each maps
	// to the string-values of its nodes once a comparison needed them.
	sets map[setKey]map[string]bool
}

// setKey identifies a node-set by its first element in memory, which the
// key keeps alive, and its length.
type setKey struct {
	first **Node
	n     int
}

// minSetSize is the least size of a node-set whose string-values a session
// collects for comparisons. Smaller node-sets are compared node by node.
const minSetSize = 16

// Constant tells s that v, the value of a variable, stays the same for
// every evaluation in it. Comparing a large node-set with = then looks the
// string-values of the other operand up instead of comparing every pair.
// Node-sets are never changed once evaluated, so this holds for any value
// the caller keeps for the session.
func (s *Session) Constant(v Value) {
	if ns, ok := v.(NodeSet); ok {
		s.keep(ns)
	}
}

func (s *Session) keep(ns NodeSet) {
	if len(ns) < minSetSize {
		return
	}
	if s.sets == nil {
		s.sets = map[setKey]map[string]bool{}
	}
	k := setKey{&ns[0], len(ns)}
	if _, ok := s.sets[k]; !ok {
		s.sets[k] = nil
	}
}

// stringSet returns the string-values of the nodes of a kept node-set,
// collecting them the first time at a step per node, or false when ns was
// not kept.
func (s *Session) stringSet(ns NodeSet) (map[string]bool, bool, error) {
	if len(ns) < minSetSize || !optimise {
		return nil, false, nil
	}
	k := setKey{&ns[0], len(ns)}
	set, ok := s.sets[k]
	if !ok || set != nil {
		return set, ok, nil
	}
	if err := s.charge(len(ns)); err != nil {
		return nil, true, err
	}
	set = make(map[string]bool, len(ns))
	for _, n := range ns {
		set[n.StringValue()] = true
	}
	s.sets[k] = set
	return set, true, nil
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
		if set[n.StringValue()] {
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

// charge counts n steps.
func (s *Session) charge(n int) error {
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
