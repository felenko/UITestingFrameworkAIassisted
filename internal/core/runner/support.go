package runner

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/felenko/uitest/internal/core/session"
)

// selectCases chooses which cases to run. An explicit id list (opts.IDs, e.g.
// from the GUI's per-case checkboxes) takes precedence; otherwise the --filter
// glob is applied (matching case id or any tag).
func (r *Runner) selectCases() []session.TestCase {
	if len(r.opts.IDs) > 0 {
		set := make(map[string]bool, len(r.opts.IDs))
		for _, id := range r.opts.IDs {
			set[id] = true
		}
		var out []session.TestCase
		for _, tc := range r.sess.TestCases {
			if set[tc.ID] {
				out = append(out, tc) // preserve session order
			}
		}
		return out
	}

	filter := strings.TrimSpace(r.opts.Filter)
	if filter == "" {
		return r.sess.TestCases
	}
	var out []session.TestCase
	for _, tc := range r.sess.TestCases {
		if matchFilter(filter, tc.ID) {
			out = append(out, tc)
			continue
		}
		for _, tag := range tc.Tags {
			if matchFilter(filter, tag) {
				out = append(out, tc)
				break
			}
		}
	}
	return out
}

func matchFilter(glob, value string) bool {
	if glob == value {
		return true
	}
	ok, err := path.Match(glob, value)
	return err == nil && ok
}

// describeMachine summarizes a step's commands for logs/events.
func describeMachine(cmds []session.Command) string {
	parts := make([]string, 0, len(cmds))
	for i := range cmds {
		parts = append(parts, cmds[i].Action)
	}
	return strings.Join(parts, ", ")
}

// describeCommand renders a one-line summary of a single command.
func describeCommand(c *session.Command) string {
	switch c.Action {
	case "mouse_move", "mouse_click", "mouse_down", "mouse_up", "mouse_scroll":
		return c.Action + " " + c.Target.Describe()
	case "mouse_drag":
		return fmt.Sprintf("mouse_drag %s → %s", c.From.Describe(), c.To.Describe())
	case "type_text":
		return fmt.Sprintf("type_text %q", truncate(c.Text, 40))
	case "key_press":
		return "key_press " + strings.Join(c.Keys, " then ")
	case "focus_window", "close_window", "move_window", "resize_window":
		return c.Action + " " + c.Target.Describe()
	case "wait":
		if c.ForAI != nil {
			return "wait forAI"
		}
		return "wait " + c.MS.String()
	case "screenshot":
		return "screenshot " + c.Target.Describe()
	case "assert_ai", "read_text_ai":
		return c.Action + " " + c.Target.Describe()
	default:
		return c.Action
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func itoa(i int) string { return strconv.Itoa(i) }
