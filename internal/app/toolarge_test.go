package app

import (
	"fmt"
	"testing"

	"qti3-validator/internal/domain/qti"
)

// A document of more nodes than the rules can hold is too large; that is no
// failure of the service.
func TestTooManyNodesIsTooLarge(t *testing.T) {
	rules := &fakeRules{err: fmt.Errorf("%w: two thousand million", ErrTooLarge)}
	v := newTestValidator(t, &fakeSchema{verdict: validVerdict}, rules)
	res := validateDoc(t, v, item)
	if res.Outcome != qti.OutcomeTooLarge || len(res.Errors) != 1 || res.Errors[0].Code != qti.CodeTooLarge {
		t.Fatalf("got %+v", res)
	}
}
