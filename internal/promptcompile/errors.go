package promptcompile

import "fmt"

// ValidationError is returned before token counting or model invocation when
// prompt components violate their structured contract.
type ValidationError struct {
	Code   string
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("promptcompile: %s: %s", e.Code, e.Detail)
}

// SplitRequiredError reports a hard-budget breach. No shortened prompt is
// returned; callers must split the evidence or task at a semantic boundary and
// compile each complete batch again.
type SplitRequiredError struct {
	Agent       Agent
	Tokens      int
	Target      int
	Hard        int
	Diagnostics Diagnostics
}

func (e *SplitRequiredError) Error() string {
	return fmt.Sprintf(
		"promptcompile: split required for %s: input=%d tokens, target=%d, hard=%d; content was not truncated",
		e.Agent,
		e.Tokens,
		e.Target,
		e.Hard,
	)
}
