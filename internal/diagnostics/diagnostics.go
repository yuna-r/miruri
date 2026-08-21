// SPDX-License-Identifier: MPL-2.0

// Package diagnostics reduces noisy compiler and linker logs into bounded,
// reproducible problem packets suitable for an automated repair agent.
package diagnostics

import (
	"fmt"
	"sort"
	"strings"
)

const (
	SchemaVersion   = "miruri.diagnostics.v1"
	DefaultMaxBytes = 20 * 1024
	maxLineBytes    = 4 * 1024
)

type Excerpt struct {
	StartLine int      `json:"start_line"`
	EndLine   int      `json:"end_line"`
	Lines     []string `json:"lines"`
}

type Report struct {
	SchemaVersion             string    `json:"schema_version"`
	RawBytes                  int       `json:"raw_bytes"`
	RawLines                  int       `json:"raw_lines"`
	SelectedBytes             int       `json:"selected_bytes"`
	SelectedLines             int       `json:"selected_lines"`
	ErrorLines                int       `json:"error_lines"`
	WarningLines              int       `json:"warning_lines"`
	SuppressedWarningLines    int       `json:"suppressed_warning_lines"`
	SuppressedNonWarningLines int       `json:"suppressed_non_warning_lines"`
	Fallback                  bool      `json:"fallback"`
	Truncated                 bool      `json:"truncated"`
	Excerpts                  []Excerpt `json:"excerpts"`
	Text                      string    `json:"text"`
}

type lineRange struct {
	start int
	end   int
}

// Summarize keeps compiler/linker failures and a small amount of local context,
// while discarding warning floods and unrelated successful command output.
func Summarize(log string, maxBytes int) Report {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	normalized := strings.ReplaceAll(log, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	report := Report{
		SchemaVersion: SchemaVersion,
		RawBytes:      len(log),
		RawLines:      len(lines),
	}
	var errorIndexes []int
	var terminationIndexes []int
	for i, line := range lines {
		if isWarningLine(line) {
			report.WarningLines++
		}
		if isPrimaryErrorLine(line) {
			report.ErrorLines++
			errorIndexes = append(errorIndexes, i)
		} else if isTerminationLine(line) {
			report.ErrorLines++
			terminationIndexes = append(terminationIndexes, i)
		}
	}

	var ranges []lineRange
	if len(errorIndexes) > 0 {
		// The nearest Miruri-recorded command is useful when a build contains
		// several configure/build phases.
		if command := nearestCommand(lines, errorIndexes[0]); command >= 0 {
			ranges = append(ranges, lineRange{start: command, end: command})
		}
		for _, index := range boundedErrorIndexes(errorIndexes, 32) {
			ranges = append(ranges, lineRange{
				start: max(0, index-2),
				end:   min(len(lines)-1, index+5),
			})
		}
		// Preserve final build-system termination lines when they are separate
		// from the compiler diagnostic itself.
		for _, i := range terminationIndexes {
			if i >= max(0, len(lines)-32) {
				ranges = append(ranges, lineRange{start: max(0, i-1), end: i})
			}
		}
	} else if len(terminationIndexes) > 0 {
		if command := nearestCommand(lines, terminationIndexes[0]); command >= 0 {
			ranges = append(ranges, lineRange{start: command, end: command})
		}
		for _, i := range boundedErrorIndexes(terminationIndexes, 16) {
			ranges = append(ranges, lineRange{start: max(0, i-3), end: i})
		}
	} else {
		report.Fallback = true
		start := max(0, len(lines)-160)
		ranges = append(ranges, lineRange{start: start, end: max(start, len(lines)-1)})
	}

	ranges = mergeRanges(ranges)
	var selectedLineIndexes = map[int]bool{}
	var excerpts []Excerpt
	used := 0
	for _, r := range ranges {
		if r.start < 0 || r.end < r.start || r.start >= len(lines) {
			continue
		}
		r.end = min(r.end, len(lines)-1)
		excerpt := Excerpt{StartLine: r.start + 1, EndLine: r.end + 1}
		for i := r.start; i <= r.end; i++ {
			line := truncateLine(lines[i])
			cost := len(line) + 1
			if used+cost > maxBytes {
				report.Truncated = true
				break
			}
			excerpt.Lines = append(excerpt.Lines, line)
			selectedLineIndexes[i] = true
			used += cost
		}
		if len(excerpt.Lines) > 0 {
			excerpt.EndLine = excerpt.StartLine + len(excerpt.Lines) - 1
			excerpts = append(excerpts, excerpt)
		}
		if report.Truncated {
			break
		}
	}
	report.Excerpts = excerpts
	report.SelectedLines = len(selectedLineIndexes)
	for i, line := range lines {
		if selectedLineIndexes[i] {
			continue
		}
		if isWarningLine(line) {
			report.SuppressedWarningLines++
		} else {
			report.SuppressedNonWarningLines++
		}
	}
	report.Text = render(report)
	report.SelectedBytes = len(report.Text)
	return report
}

func render(report Report) string {
	var out strings.Builder
	fmt.Fprintf(&out, "Miruri structured build diagnostics\n")
	fmt.Fprintf(&out, "- raw log: %d bytes / %d lines\n", report.RawBytes, report.RawLines)
	fmt.Fprintf(&out, "- detected: %d error line(s), %d warning line(s)\n", report.ErrorLines, report.WarningLines)
	fmt.Fprintf(&out, "- suppressed: %d warning line(s), %d other line(s)\n", report.SuppressedWarningLines, report.SuppressedNonWarningLines)
	if report.Fallback {
		fmt.Fprintln(&out, "- note: no canonical compiler/linker error was recognized; a bounded tail excerpt is shown")
	}
	if report.Truncated {
		fmt.Fprintln(&out, "- note: selected diagnostics were truncated to the configured byte budget")
	}
	fmt.Fprintln(&out, "- the complete unmodified log remains in the Miruri artifact set as build.log")
	for _, excerpt := range report.Excerpts {
		fmt.Fprintf(&out, "\n--- build.log lines %d-%d ---\n", excerpt.StartLine, excerpt.EndLine)
		for _, line := range excerpt.Lines {
			fmt.Fprintln(&out, line)
		}
	}
	return strings.TrimSpace(out.String()) + "\n"
}

func boundedErrorIndexes(indexes []int, limit int) []int {
	if len(indexes) <= limit || limit <= 0 {
		return append([]int(nil), indexes...)
	}
	front := limit / 2
	back := limit - front
	out := append([]int(nil), indexes[:front]...)
	out = append(out, indexes[len(indexes)-back:]...)
	return out
}

func nearestCommand(lines []string, before int) int {
	for i := min(before, len(lines)-1); i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "$ ") {
			return i
		}
	}
	return -1
}

func mergeRanges(ranges []lineRange) []lineRange {
	if len(ranges) == 0 {
		return nil
	}
	sort.Slice(ranges, func(i, j int) bool {
		if ranges[i].start == ranges[j].start {
			return ranges[i].end < ranges[j].end
		}
		return ranges[i].start < ranges[j].start
	})
	out := []lineRange{ranges[0]}
	for _, current := range ranges[1:] {
		last := &out[len(out)-1]
		if current.start <= last.end+1 {
			last.end = max(last.end, current.end)
			continue
		}
		out = append(out, current)
	}
	return out
}

func isWarningLine(line string) bool {
	lower := strings.ToLower(line)
	return strings.Contains(lower, "warning:") ||
		strings.Contains(lower, ": warning ") ||
		strings.Contains(lower, " warning c")
}

func isPrimaryErrorLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return false
	}
	markers := []string{
		"fatal error:",
		": error:",
		": error ",
		" error c",
		"undefined reference",
		"unresolved external symbol",
		"linker command failed",
		"collect2: error",
		"cmake error",
		"no rule to make target",
		"cannot find -l",
		"ld: error",
		"ld.lld: error",
		"lld-link: error",
		"clang: error:",
		"clang++: error:",
		"gcc: error:",
		"g++: error:",
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.HasPrefix(lower, "error:") || strings.HasPrefix(lower, "failed:") {
		return true
	}
	return false
}

func isTerminationLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return strings.Contains(lower, "make: ***") ||
		strings.Contains(lower, "ninja: build stopped") ||
		strings.Contains(lower, "build stopped: subcommand failed") ||
		strings.Contains(lower, "command failed: exit status") ||
		strings.Contains(lower, "msbuild : error")
}

func truncateLine(line string) string {
	if len(line) <= maxLineBytes {
		return line
	}
	return line[:maxLineBytes-24] + " [line truncated]"
}
