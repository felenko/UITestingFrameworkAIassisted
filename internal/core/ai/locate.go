package ai

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LocateResult is the outcome of a locate (find:) call: where the AI says the
// described element is, in image pixel coordinates.
type LocateResult struct {
	Provider  string
	Model     string
	Prompt    string
	RawAnswer string
	X, Y      int
	Found     bool // false when the AI answered NOT FOUND
	Retries   int
	Err       error
}

// LocatePoint asks the AI to locate a described UI element in a screenshot and
// return its center as image pixel coordinates. imgW/imgH anchor the coordinate
// space in the prompt and bound-check the answer (an out-of-range point is
// treated as unparseable and retried).
func (e *Engine) LocatePoint(ctx context.Context, r Request, imgW, imgH int) LocateResult {
	adapter, cfg, err := e.resolve(r)
	if err != nil {
		return LocateResult{Err: err}
	}
	prompt := buildLocatePrompt(cfg.Provider, r.Question, r.ImagePath, imgW, imgH)
	res := LocateResult{Provider: cfg.Provider, Model: cfg.Model, Prompt: prompt}

	attempts := cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			res.Retries++
			backoff := time.Duration(attempt) * 500 * time.Millisecond
			e.logf("debug", fmt.Sprintf("AI locate retry %d after %v: %v", attempt, backoff, lastErr))
			select {
			case <-ctx.Done():
				res.Err = ctx.Err()
				return res
			case <-time.After(backoff):
			}
		}
		out, rerr := e.runWithTimeout(ctx, adapter, cfg, prompt, r.ImagePath)
		if rerr != nil {
			lastErr = rerr
			continue
		}
		out = decodeProviderStdout(cfg.Provider, out)
		res.RawAnswer = out
		x, y, found, perr := ParsePoint(out)
		if perr != nil {
			lastErr = perr
			continue
		}
		if !found {
			res.Found = false // authoritative NOT FOUND: no retry
			return res
		}
		if x < 0 || y < 0 || (imgW > 0 && x >= imgW) || (imgH > 0 && y >= imgH) {
			lastErr = fmt.Errorf("located point (%d,%d) is outside the %dx%d image", x, y, imgW, imgH)
			continue
		}
		res.X, res.Y, res.Found = x, y, true
		return res
	}
	res.Err = lastErr
	return res
}

var pointRe = regexp.MustCompile(`(?i)point\s*:\s*\(?\s*(\d+)\s*[,;x]\s*(\d+)`)

// ParsePoint extracts a "POINT: x,y" sentinel (or "NOT FOUND") from the
// provider's raw answer, scanning bottom-up since it is requested as the final
// line. found=false with nil err means an explicit NOT FOUND.
func ParsePoint(raw string) (x, y int, found bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, 0, false, fmt.Errorf("empty locate answer")
	}
	lines := strings.Split(raw, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := stripMarkdown(lines[i])
		if line == "" {
			continue
		}
		if m := pointRe.FindStringSubmatch(line); m != nil {
			px, ex := strconv.Atoi(m[1])
			py, ey := strconv.Atoi(m[2])
			if ex != nil || ey != nil {
				continue
			}
			return px, py, true, nil
		}
		if strings.Contains(strings.ToLower(line), "not found") {
			return 0, 0, false, nil
		}
	}
	return 0, 0, false, fmt.Errorf("unparseable locate answer from %q", truncateForError(raw, 240))
}

func buildLocatePrompt(provider, description, imagePath string, imgW, imgH int) string {
	description = strings.TrimSpace(description)
	switch provider {
	case "cursor":
		// Single line on purpose (see buildAssertPrompt): avoids batch-wrapper newline truncation.
		d := strings.Join(strings.Fields(description), " ")
		return fmt.Sprintf("You are a precise UI element locator. Open the image at %s (it is exactly %d x %d pixels, origin 0,0 at the top-left). Locate this element: %s. Output only one line and nothing else: POINT: x,y with the pixel coordinates of the element's center, or NOT FOUND if it is not visible.", imagePath, imgW, imgH, d)
	default:
		return fmt.Sprintf(`You are a precise UI element locator.

You are looking at a screenshot saved at this path: %s
Open and view that image file. It is exactly %d x %d pixels; pixel (0,0) is the top-left corner.

Locate this element: %s

After any explanation, your reply MUST end with a final line in exactly one of these formats:
POINT: x,y
or
NOT FOUND
where x,y is the pixel position of the CENTER of the element.`, imagePath, imgW, imgH, description)
	}
}
