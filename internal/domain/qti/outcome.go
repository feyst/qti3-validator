package qti

// Outcome classifies a validation result.
type Outcome int

// The outcomes, in increasing severity.
const (
	OutcomeValid      Outcome = iota
	OutcomeInvalid            // well-formed XML that is not acceptable QTI
	OutcomeTooLarge           // input exceeds a configured size limit
	OutcomeMalformed          // not well-formed XML, or not a readable ZIP
	OutcomeTooComplex         // validation stopped: too much work, or out of time
	OutcomeInternal           // the validator failed
)

// Worse returns the more severe of o and other.
func (o Outcome) Worse(other Outcome) Outcome {
	return max(o, other)
}
