// SPDX-License-Identifier: MPL-2.0

package diagnostics

import (
	"fmt"
	"strings"
	"testing"
)

func TestSummarizeSuppressesWarningFloodAndKeepsError(t *testing.T) {
	var log strings.Builder
	fmt.Fprintln(&log, "$ clang -arch arm64 -c src/fzy.c")
	for i := 0; i < 213; i++ {
		fmt.Fprintf(&log, "/usr/local/include/stdlib.h:%d:1: warning: pointer is missing a nullability type specifier [-Wnullability-completeness]\n", i+1)
	}
	fmt.Fprintln(&log, "src/fzy.c:2:2: error: \"Miruri demo: this source currently supports x86 only\"")
	fmt.Fprintln(&log, "#error \"Miruri demo: this source currently supports x86 only\"")
	fmt.Fprintln(&log, " ^")
	fmt.Fprintln(&log, "213 warnings and 1 error generated.")
	fmt.Fprintln(&log, "make: *** [src/fzy.o] Error 1")

	report := Summarize(log.String(), DefaultMaxBytes)
	if report.WarningLines != 213 {
		t.Fatalf("warning count=%d", report.WarningLines)
	}
	if report.SuppressedWarningLines < 200 {
		t.Fatalf("warning flood was not suppressed: %+v", report)
	}
	if !strings.Contains(report.Text, "src/fzy.c:2:2: error") {
		t.Fatalf("primary error missing:\n%s", report.Text)
	}
	if strings.Count(report.Text, "nullability type specifier") > 2 {
		t.Fatalf("warning flood leaked into summary:\n%s", report.Text)
	}
	if len(report.Text) >= len(log.String())/4 {
		t.Fatalf("summary did not compact enough: raw=%d selected=%d", len(log.String()), len(report.Text))
	}
}

func TestSummarizeFallsBackToBoundedTail(t *testing.T) {
	var log strings.Builder
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&log, "ordinary output %d\n", i)
	}
	report := Summarize(log.String(), 2048)
	if !report.Fallback {
		t.Fatal("expected fallback")
	}
	if !report.Truncated {
		t.Fatal("expected byte-budget truncation")
	}
	if len(report.Text) > 4096 {
		t.Fatalf("fallback summary unexpectedly large: %d", len(report.Text))
	}
}
