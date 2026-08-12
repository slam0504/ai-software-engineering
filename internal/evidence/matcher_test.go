package evidence

import (
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/plan"
)

// TestClassify covers the §3.7 outcome table shared by ClassifyExpectedRed
// and ClassifyNegativeControl (both delegate to the same classify logic):
// a red-state characteristic match → passed; exit 0 → failed (the test
// suite did not fail); exit != 0 with a mismatched characteristic (e.g. a
// compile error carrying none of the declared failure signature) → error.
func TestClassify(t *testing.T) {
	ef := plan.ExpectedFailure{TestIDs: []string{"TestFoo", "TestBar"}, Matcher: "FAIL"}

	cases := []struct {
		name       string
		exitCode   int
		output     string
		wantResult string
	}{
		{
			name:       "red state characteristic matches -> passed",
			exitCode:   1,
			output:     "=== RUN TestFoo\n--- FAIL: TestFoo\n=== RUN TestBar\n--- FAIL: TestBar\nFAIL\n",
			wantResult: "passed",
		},
		{
			name:       "exit 0 -> failed (tests did not fail)",
			exitCode:   0,
			output:     "--- FAIL: TestFoo\n--- FAIL: TestBar\nFAIL\n", // even if the text looks red, exit 0 wins
			wantResult: "failed",
		},
		{
			name:       "compile error output carries no matcher characteristic -> error",
			exitCode:   2,
			output:     "# pkg\n./foo.go:10:2: undefined: bar\nFAIL\tpkg [build failed]\n",
			wantResult: "error",
		},
		{
			name:       "exit != 0, matcher present but a declared test id is missing -> error",
			exitCode:   1,
			output:     "--- FAIL: TestFoo\nFAIL\n", // TestBar never showed up
			wantResult: "error",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotResult, gotObserved := ClassifyExpectedRed(c.exitCode, []byte(c.output), ef)
			if gotResult != c.wantResult {
				t.Errorf("ClassifyExpectedRed(%d, %q) result = %q, want %q (observed=%q)", c.exitCode, c.output, gotResult, c.wantResult, gotObserved)
			}
			if gotResult == "passed" && gotObserved == "" {
				t.Errorf("ClassifyExpectedRed: passed classification must carry a non-empty observed summary")
			}

			// ClassifyNegativeControl shares the exact same judgment (§3.7:
			// "同 expected-red 判準") — assert both functions agree.
			ncResult, ncObserved := ClassifyNegativeControl(c.exitCode, []byte(c.output), ef)
			if ncResult != gotResult {
				t.Errorf("ClassifyNegativeControl result = %q, want %q (same as ClassifyExpectedRed)", ncResult, gotResult)
			}
			if c.wantResult == "passed" && ncObserved != gotObserved {
				t.Errorf("ClassifyNegativeControl observed = %q, want %q (same as ClassifyExpectedRed)", ncObserved, gotObserved)
			}
		})
	}
}

func TestClassifyExpectedRed_ObservedIsMatcherHitLine(t *testing.T) {
	ef := plan.ExpectedFailure{TestIDs: []string{"TestX"}, Matcher: "FAIL: TestX"}
	output := "=== RUN TestX\nFAIL: TestX (0.00s)\nFAIL\n"
	result, observed := ClassifyExpectedRed(1, []byte(output), ef)
	if result != "passed" {
		t.Fatalf("result = %q, want passed", result)
	}
	if !strings.Contains(observed, "FAIL: TestX") {
		t.Errorf("observed = %q, want the matcher-hit line", observed)
	}
}
