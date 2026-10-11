package app

import (
	"context"
	"errors"
	"fmt"

	"qti3-validator/internal/domain/qti"
)

// What a document takes in memory before its rules run: the document itself,
// in a buffer that grew to hold it, and the schema check, which reads it as
// a stream and keeps little. Measured, the two take at most 4.8 times the
// size of the document, for one that is a single run of text.
const (
	readMemoryBase    = 1 << 20
	readMemoryPerByte = 6
)

func memoryToRead(size int64) int64 { return readMemoryBase + readMemoryPerByte*size }

// reserveToRead reserves what reading a document of up to size bytes and
// checking it against its schema takes. On failure it returns the finding
// to report, with the outcome it implies.
func reserveToRead(ctx context.Context, size int64) (qti.Finding, qti.Outcome, bool) {
	m := MemoryFrom(ctx)
	if m == nil {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	need := memoryToRead(size)
	err := m.Reserve(ctx, need)
	if err == nil {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	f, outcome := memoryFinding(err, fmt.Sprintf(
		"the document of %s needs about %s of memory to read and to check against its schema; this service allows %s for one document (MAX_VALIDATION_MEMORY)",
		mebibytes(size), mebibytes(need), mebibytes(m.Limit())))
	return f, outcome, false
}

// reserveToCheck reserves what checking a document takes from here on, as
// soon as it is read and before the schema check: the most of what the
// schema check takes and of what the rules take, which the nodes of the
// document decide and one pass over its markup tells. So a validation waits
// for memory once, with little done, and never after a schema check of
// seconds; and a document whose rules would take more than a validation may
// reserve is refused at once, for what it would take if it were checked,
// also when it would not pass its schema.
//
// When there is no room now, the validation waits for it without the
// document: it lets go of the bytes it read and of what it reserved, and
// reads the document again once it has its room, from the upload or the
// package, which are on disk. So a validation that waits holds no memory,
// and those that run stay within what the service allows however many
// wait. A document that cannot be read again cannot wait, and stops.
//
// On failure it returns the finding to report, with the outcome it implies.
func reserveToCheck(ctx context.Context, rules RuleChecker, src *source) (qti.Finding, qti.Outcome, bool) {
	m := MemoryFrom(ctx)
	estimator, ok := rules.(MemoryEstimator)
	if m == nil || !ok {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	estimate := estimator.EstimateMemory(src.data)
	need := max(estimate.Bytes, memoryToRead(int64(len(src.data))))
	if estimate.Bytes <= 0 || m.TryReserve(need) {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	err := ErrMemoryBusy
	if estimate.Bytes > m.Limit() {
		err = ErrMemoryExceeded
	} else if src.reread != nil {
		src.data = nil
		err = m.Reserve(ctx, need)
	}
	if err != nil {
		f, outcome := memoryFinding(err, exceededMessage(estimate, m.Limit()))
		return f, outcome, false
	}
	data, failure := src.reread()
	if failure != nil {
		return failure.Errors[0], failure.Outcome, false
	}
	src.data = data
	return qti.Finding{}, qti.OutcomeValid, true
}

func exceededMessage(estimate MemoryEstimate, limit int64) string {
	return fmt.Sprintf(
		"the document has %s and needs about %s of memory to check; this service allows %s for one document (MAX_VALIDATION_MEMORY)",
		nodeCount(estimate.Nodes), mebibytes(estimate.Bytes), mebibytes(limit))
}

// reserveForRules sets what the validation reserves to what the rules take,
// now that the schema check is over. That is no more than reserveToCheck
// reserved, so it does not wait.
func reserveForRules(ctx context.Context, rules RuleChecker, src *source) (qti.Finding, qti.Outcome, bool) {
	m := MemoryFrom(ctx)
	estimator, ok := rules.(MemoryEstimator)
	if m == nil || !ok {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	estimate := estimator.EstimateMemory(src.data)
	if estimate.Bytes <= 0 || m.TryReserve(estimate.Bytes) {
		return qti.Finding{}, qti.OutcomeValid, true
	}
	f, outcome := memoryFinding(ErrMemoryBusy, "")
	return f, outcome, false
}

// releaseMemory gives back what the validation reserved.
func releaseMemory(ctx context.Context) {
	if m := MemoryFrom(ctx); m != nil {
		m.TryReserve(0)
	}
}

// memoryFinding is the finding for a reservation that failed with err:
// exceeded names the numbers of a document that can never fit, which is too
// large; any other failure stopped the validation.
func memoryFinding(err error, exceeded string) (qti.Finding, qti.Outcome) {
	if errors.Is(err, ErrMemoryExceeded) {
		return qti.Finding{Code: qti.CodeTooLarge, Message: exceeded}, qti.OutcomeTooLarge
	}
	if stop, ok := stopFinding(err); ok {
		return stop, qti.OutcomeTooComplex
	}
	return qti.Finding{Code: qti.CodeInternal, Message: "reserve memory: " + err.Error()}, qti.OutcomeInternal
}

// mebibytes writes a number of bytes for a message: "950 MiB", "1.4 MiB".
func mebibytes(n int64) string {
	const mib = 1 << 20
	switch {
	case n >= 10*mib:
		return fmt.Sprintf("%d MiB", (n+mib/2)/mib)
	case n >= mib/10:
		return fmt.Sprintf("%.1f MiB", float64(n)/mib)
	}
	return fmt.Sprintf("%d KiB", (n+512)/1024)
}

// nodeCount writes a number of nodes for a message: "3.9 million nodes".
func nodeCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1f million nodes", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%d thousand nodes", (n+500)/1000)
	case n == 1:
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}
