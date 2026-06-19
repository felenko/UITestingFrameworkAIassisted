package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/session"
)

// runValidation evaluates every assert entry; the case passes only if all pass.
func (r *Runner) runValidation(ctx context.Context, tc *session.TestCase) result.Validation {
	// Let the UI settle (async input/repaint) before capturing evidence.
	time.Sleep(r.scale(350 * time.Millisecond))
	vr := result.Validation{Human: tc.Validation.Human, Status: result.StatusPassed}
	for i := range tc.Validation.Assert {
		ar := r.runAssert(ctx, tc.ID, &tc.Validation.Assert[i], i)
		vr.Assert = append(vr.Assert, ar)
		vr.Status = worseStatus(vr.Status, ar.Status)
	}
	return vr
}

// runAssert evaluates a single assertion and always produces expected + actual
// blocks (the report invariant).
func (r *Runner) runAssert(ctx context.Context, caseID string, a *session.Command, index int) result.Assert {
	assertID := a.ID
	if assertID == "" {
		assertID = fmt.Sprintf("assert-%02d", index)
	}

	ar := result.Assert{
		ID:       assertID,
		Human:    a.Human,
		Action:   a.Action,
		Question: r.bag.Expand(a.Question),
		Expect:   normalizeExpect(a.Expect),
		Provider: r.providerFor(a),
		Model:    r.model,
	}

	// Capture the actual screenshot. Deterministic asserts capture the whole
	// screen: it always succeeds (even when the asserted window is absent, e.g.
	// expect:no) and shows any dialog that popped up, keeping visual evidence.
	actualName := fmt.Sprintf("%s_%s_actual.png", caseID, assertID)
	capTarget := a.Target
	if isDeterministicAssert(a.Action) {
		capTarget = nil
	}
	actualRel, capErr := r.captureAndSave(capTarget, actualName)
	ar.Actual = result.Actual{Image: actualRel, CapturedAt: time.Now()}
	r.bus.Publish(event.Event{Type: event.ScreenshotCaptured, CaseID: caseID, Path: actualRel, Which: "actual"})

	// Resolve the expected ("what should be") image.
	ar.Expected = r.resolveExpected(caseID, assertID, a, actualRel)
	if ar.Expected.Source == "candidate" {
		// Flag for the run-level "unverified baselines" banner via the results.
		r.usedCandidate = true
	}

	if capErr != nil {
		ar.Status = result.StatusError
		ar.Error = "capture failed: " + capErr.Error()
		r.logf("error", "assert %s: %v", assertID, capErr)
		r.publishAssert(caseID, &ar)
		return ar
	}

	// Deterministic asserts: evaluate against the UIA tree / window list (no AI).
	if isDeterministicAssert(a.Action) {
		r.evalDeterministic(&ar, a)
		r.logf("info", "  assert %s: %s (verdict=%v, expect=%s) [deterministic] %s",
			assertID, ar.Status, ar.Verdict, ar.Expect, ar.RawAnswer)
		r.publishAssert(caseID, &ar)
		return ar
	}

	// Run the AI verdict.
	switch a.Action {
	case "read_text_ai":
		value, raw, err := r.engine.ReadText(ctx, r.aiRequest(a, actualRel))
		ar.RawAnswer = raw
		if err != nil {
			ar.Status = result.StatusError
			ar.Error = err.Error()
		} else {
			if a.Store != "" {
				r.bag.Set(a.Store, value)
			}
			ar.Verdict = true
			ar.Status = result.StatusPassed
		}
	default: // assert_ai
		out := r.engine.AssertAI(ctx, r.aiRequest(a, actualRel))
		ar.RawAnswer = out.RawAnswer
		ar.Verdict = out.Verdict
		ar.Samples = out.Samples
		ar.Retries = out.Retries
		switch {
		case out.Err != nil:
			ar.Status = result.StatusError
			ar.Error = out.Err.Error()
		case out.Pass:
			ar.Status = result.StatusPassed
		default:
			ar.Status = result.StatusFailed
		}
	}

	r.logf("info", "  assert %s: %s (verdict=%v, expect=%s)", assertID, ar.Status, ar.Verdict, ar.Expect)
	r.publishAssert(caseID, &ar)
	return ar
}

func (r *Runner) publishAssert(caseID string, ar *result.Assert) {
	r.bus.Publish(event.Event{
		Type: event.AssertFinished, CaseID: caseID, Question: ar.Question, Expect: ar.Expect,
		RawAnswer: ar.RawAnswer, Verdict: ar.Verdict, Status: string(ar.Status),
		ExpectedPath: ar.Expected.Image, ActualPath: ar.Actual.Image,
	})
}

// resolveExpected applies the resolution order: declared baseline → approved
// baseline → first-run candidate (docs/05 §3). The chosen image is copied into
// the screenshots dir so the report is self-contained.
func (r *Runner) resolveExpected(caseID, assertID string, a *session.Command, actualRel string) result.Expected {
	dstName := fmt.Sprintf("%s_%s_expected.png", caseID, assertID)
	sessionDir := filepath.Dir(r.sess.SourcePath)

	// 1. Declared baseline.
	if a.Baseline != "" {
		src := baselineRel(sessionDir, r.bag.Expand(a.Baseline))
		if rel, err := r.copyInto(src, dstName); err == nil {
			return result.Expected{Source: "declared", Image: rel}
		}
		r.logf("warn", "assert %s: declared baseline %q not found, falling back", assertID, src)
	}

	// 2. Approved baseline in <sessionDir>/baselines/<case>/<assert>.png.
	approved := filepath.Join(r.baselineDir, caseID, assertID+".png")
	if rel, err := r.copyInto(approved, dstName); err == nil {
		return result.Expected{Source: "approved", Image: rel}
	}

	// 3. First-run candidate: reuse the actual capture.
	if actualRel != "" {
		if rel, err := r.copyInto(r.absPath(actualRel), dstName); err == nil {
			return result.Expected{Source: "candidate", Image: rel}
		}
		return result.Expected{Source: "candidate", Image: actualRel}
	}
	return result.Expected{Source: "candidate", Image: ""}
}

// copyInto copies src into the screenshots dir as name; returns outDir-relative path.
func (r *Runner) copyInto(src, name string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	dst := filepath.Join(r.shotsDir, name)
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return "", err
	}
	rel, err := filepath.Rel(r.outDir, dst)
	if err != nil {
		rel = filepath.Join("screenshots", name)
	}
	return filepath.ToSlash(rel), nil
}

func (r *Runner) providerFor(a *session.Command) string {
	if a.Provider != "" {
		return a.Provider
	}
	return r.provider
}

func (r *Runner) skippedAssert(a *session.Command, index int) result.Assert {
	id := a.ID
	if id == "" {
		id = fmt.Sprintf("assert-%02d", index)
	}
	return result.Assert{
		ID: id, Human: a.Human, Action: a.Action,
		Question: r.bag.Expand(a.Question), Expect: normalizeExpect(a.Expect),
		Provider: r.providerFor(a), Status: result.StatusSkipped,
		Expected: result.Expected{Source: "candidate"},
	}
}

func normalizeExpect(expect string) string {
	switch expect {
	case "no", "false":
		return "no"
	default:
		return "yes"
	}
}

// worseStatus returns the more severe of two statuses (error > failed > passed).
func worseStatus(a, b result.Status) result.Status {
	rank := func(s result.Status) int {
		switch s {
		case result.StatusError:
			return 3
		case result.StatusFailed:
			return 2
		case result.StatusSkipped:
			return 1
		default:
			return 0
		}
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}
