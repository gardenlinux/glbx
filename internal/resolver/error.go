package resolver

import (
	"fmt"
	"strings"
)

// ResolutionError is returned when dependency resolution fails. It provides
// structured information about the exploration tree, showing which packages
// were tried at each decision point and why they failed.
type ResolutionError struct {
	Attempts []Attempt // what was tried at each decision point
}

// Attempt represents one exploration branch in the resolution tree.
type Attempt struct {
	Package   string    // package name at this decision point
	Reason    string    // why this candidate failed
	Chain     []string  // dependency chain that led here
	SubErrors []Attempt // nested resolution failures (alternatives tried)
}

// Error implements the error interface with a structured, human-readable
// representation of the full resolution failure tree.
func (e *ResolutionError) Error() string {
	var b strings.Builder
	b.WriteString("dependency resolution failed\n")

	for _, attempt := range e.Attempts {
		writeAttempt(&b, attempt, 1)
	}

	return strings.TrimRight(b.String(), "\n")
}

// writeAttempt recursively writes an attempt tree with indentation.
func writeAttempt(b *strings.Builder, a Attempt, depth int) {
	indent := strings.Repeat("  ", depth)

	if len(a.SubErrors) > 0 {
		b.WriteString(fmt.Sprintf("%s%s %s:\n", indent, a.Package, a.Reason))
		for _, sub := range a.SubErrors {
			writeAttempt(b, sub, depth+1)
		}
	} else {
		if a.Chain != nil && len(a.Chain) > 0 {
			b.WriteString(fmt.Sprintf("%s%s: %s (via %s)\n", indent, a.Package, a.Reason, strings.Join(a.Chain, " -> ")))
		} else {
			b.WriteString(fmt.Sprintf("%s%s: %s\n", indent, a.Package, a.Reason))
		}
	}
}

// FormatChain returns a human-readable representation of a dependency chain.
func FormatChain(chain []string) string {
	return strings.Join(chain, " -> ")
}
