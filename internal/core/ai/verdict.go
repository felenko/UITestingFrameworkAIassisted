package ai

import (
	"fmt"
	"strings"
)

// ParseVerdict normalizes a provider's raw stdout into a boolean (docs/02 §4.4).
// It returns an error for unparseable/ambiguous answers so the engine can treat
// them as a step error rather than a silent pass.
func ParseVerdict(raw string) (bool, error) {
	line := firstMeaningfulLine(raw)
	token := firstToken(line)
	switch token {
	case "yes", "true", "pass", "passed", "1", "y":
		return true, nil
	case "no", "false", "fail", "failed", "0", "n":
		return false, nil
	}
	// Fall back to scanning the whole first line for an unambiguous keyword.
	low := strings.ToLower(line)
	hasYes := strings.Contains(low, "yes")
	hasNo := strings.Contains(low, "no")
	switch {
	case hasYes && !hasNo:
		return true, nil
	case hasNo && !hasYes:
		return false, nil
	}
	return false, fmt.Errorf("unparseable verdict from %q", strings.TrimSpace(raw))
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
