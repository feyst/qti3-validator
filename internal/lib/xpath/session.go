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
	if s.memo == nil {
		s.memo = map[memoKey]NodeSet{}
	}
	s.memo[k] = ns
	return ns, nil
}
