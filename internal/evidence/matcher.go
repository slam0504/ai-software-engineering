package evidence

import (
	"bytes"
	"fmt"

	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// ClassifyExpectedRed classifies a run's outcome against an approved
// TestContract's ExpectedFailure per §3.7:
//
//   - exit != 0, output contains ef.Matcher, and every ef.TestIDs entry is
//     present in output → "passed" (the red-state characteristic was
//     reproduced); observed is the matcher-hit line summary.
//   - exit == 0 → "failed" (the test did not fail as required pre-implementation).
//   - exit != 0 but the failure characteristic does not match (e.g. a
//     compile error with a different signature, or a matching matcher but a
//     missing test id) → "error".
func ClassifyExpectedRed(exitCode int, output []byte, ef plan.ExpectedFailure) (result, observed string) {
	return classify(exitCode, output, ef)
}

// ClassifyNegativeControl uses the identical criteria as ClassifyExpectedRed
// (§3.7): a negative-control mutation must be caught by the very same test
// set that the task's contract declares, so "passed" here means the
// mutation was correctly detected as a regression.
func ClassifyNegativeControl(exitCode int, output []byte, ef plan.ExpectedFailure) (result, observed string) {
	return classify(exitCode, output, ef)
}

// classify is the shared judgment behind both exported Classify* functions.
func classify(exitCode int, output []byte, ef plan.ExpectedFailure) (result, observed string) {
	if exitCode == 0 {
		return "failed", "tests did not fail"
	}
	matcher := []byte(ef.Matcher)
	if !bytes.Contains(output, matcher) {
		return "error", fmt.Sprintf("matcher %q not found in output", ef.Matcher)
	}
	var missing []string
	for _, id := range ef.TestIDs {
		if !bytes.Contains(output, []byte(id)) {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return "error", fmt.Sprintf("expected test ids not found in output: %v", missing)
	}
	return "passed", matchSummary(output, ef.Matcher)
}

// matchSummary returns the first output line containing matcher, trimmed —
// the "matcher 命中行摘要" the brief calls for as the observed-failure value
// on a passed classification. Falls back to matcher itself if, somehow, no
// single line contains it (e.g. the match spans a line break).
func matchSummary(output []byte, matcher string) string {
	for _, line := range bytes.Split(output, []byte("\n")) {
		if bytes.Contains(line, []byte(matcher)) {
			return string(bytes.TrimSpace(line))
		}
	}
	return matcher
}
