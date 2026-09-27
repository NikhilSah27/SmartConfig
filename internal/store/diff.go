package store

import (
	"bytes"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// UnifiedDiff returns a unified diff from a to b, or "" when they are equal.
func UnifiedDiff(a, b []byte, aLabel, bLabel string) (string, error) {
	if bytes.Equal(a, b) {
		return "", nil
	}
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitLines(a),
		B:        splitLines(b),
		FromFile: aLabel,
		ToFile:   bLabel,
		Context:  3,
	})
}

// splitLines keeps line endings. A final line without a newline gets the
// same marker diff(1) prints, so a missing trailing newline shows up as a
// change instead of being hidden.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(b), "\n")
	if lines[len(lines)-1] == "" {
		return lines[:len(lines)-1]
	}
	lines[len(lines)-1] += "\n\\ No newline at end of file\n"
	return lines
}
