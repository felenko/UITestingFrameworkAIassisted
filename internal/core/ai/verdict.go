package ai

import (
	"fmt"
	"strings"
)

// ParseVerdict normalizes a provider's raw stdout into a boolean (docs/02 §4.4).
// It returns an error for unparseable/ambiguous answers so the engine can treat
// them as a step error rather than a silent pass.
func ParseVerdict(raw string) (bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, fmt.Errorf("unparseable verdict from %q", raw)
	}

	lines := strings.Split(raw, "\n")
	// 1) Prefer an explicit "VERDICT: YES/NO" sentinel (won't collide with prose
	// like "no rows"). Scan bottom-up since it is requested as the final line.
	for i := len(lines) - 1; i >= 0; i-- {
		if v, ok := verdictFromSentinel(lines[i]); ok {
			return v, nil
		}
	}
	// 2) A standalone YES/NO line (the whole line is just the token).
	for i := len(lines) - 1; i >= 0; i-- {
		if v, ok := verdictFromStandalone(lines[i]); ok {
			return v, nil
		}
	}
	// 3) Last resort: the first meaningful line begins with an unambiguous token.
	if v, ok := verdictFromLine(firstMeaningfulLine(raw)); ok {
		return v, nil
	}
	return false, fmt.Errorf("unparseable verdict from %q", truncateForError(raw, 240))
}

// verdictFromSentinel matches a "VERDICT: YES" / "VERDICT: NO" line.
func verdictFromSentinel(line string) (bool, bool) {
	low := strings.ToLower(stripMarkdown(line))
	idx := strings.Index(low, "verdict:")
	if idx < 0 {
		return false, false
	}
	rest := strings.TrimSpace(low[idx+len("verdict:"):])
	switch firstToken(rest) {
	case "yes", "true", "pass", "passed", "y":
		return true, true
	case "no", "false", "fail", "failed", "n":
		return false, true
	}
	return false, false
}

// verdictFromStandalone matches a line whose entire content is a verdict token.
func verdictFromStandalone(line string) (bool, bool) {
	low := stripMarkdown(line)
	low = strings.Trim(strings.ToLower(low), ".,;:!?\"' \t")
	switch low {
	case "yes", "true", "pass", "passed", "y":
		return true, true
	case "no", "false", "fail", "failed", "n":
		return false, true
	}
	return false, false
}

func verdictFromLine(line string) (bool, bool) {
	line = stripMarkdown(line)
	if line == "" {
		return false, false
	}
	token := firstToken(line)
	switch token {
	case "yes", "true", "pass", "passed", "1", "y":
		return true, true
	case "no", "false", "fail", "failed", "0", "n":
		return false, true
	}
	low := strings.ToLower(line)
	hasYes := strings.Contains(low, "yes")
	hasNo := containsNo(low)
	switch {
	case hasYes && !hasNo:
		return true, true
	case hasNo && !hasYes:
		return false, true
	}
	return false, false
}

func stripMarkdown(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "## ")
	line = strings.ReplaceAll(line, "**", "")
	line = strings.Trim(line, "*#_`> \t")
	return strings.TrimSpace(line)
}

// containsNo reports whether the line contains "no" as a word (not inside "not").
func containsNo(low string) bool {
	for _, w := range strings.Fields(low) {
		w = strings.Trim(w, ".,;:!?\"'")
		if w == "no" {
			return true
		}
	}
	return false
}

func truncateForError(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func firstMeaningfulLine(raw string) string {
	for _, l := range strings.Split(raw, "\n") {
		if strings.TrimSpace(l) != "" {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

func firstToken(line string) string {
	line = strings.ToLower(strings.TrimSpace(line))
	line = strings.TrimLeft(line, "*#-> \t")
	// Cut at the first non-letter/digit so "YES." or "YES," parse correctly.
	end := len(line)
	for i, r := range line {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			end = i
			break
		}
	}
	return line[:end]
}

// majority returns the most common boolean among verdicts and whether a clear
// winner exists.
func majority(verdicts []bool) (bool, bool) {
	if len(verdicts) == 0 {
		return false, false
	}
	yes, no := 0, 0
	for _, v := range verdicts {
		if v {
			yes++
		} else {
			no++
		}
	}
	if yes == no {
		return false, false
	}
	return yes > no, true
}
