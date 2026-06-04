// Package ai is the AI assertion engine (docs/02 §4): it builds the prompt with
// a strict answer contract, invokes a provider CLI with a screenshot, parses
// the verdict, and applies reliability controls (retries, majority vote,
// caching, timeouts).
package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Config configures the engine defaults (from session.ai + overrides).
type Config struct {
	Provider string
	Model    string
	Timeout  time.Duration
	Retries  int
	Samples  int
}

// Engine runs AI questions against screenshots.
type Engine struct {
	cfg     Config
	logf    func(level, msg string)
	mu      sync.Mutex
	cache   map[string]cachedVerdict
}

type cachedVerdict struct {
	raw     string
	verdict bool
	err     error
}

// New builds an engine. logf may be nil.
func New(cfg Config, logf func(level, msg string)) *Engine {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Samples < 1 {
		cfg.Samples = 1
	}
	if logf == nil {
		logf = func(string, string) {}
	}
	return &Engine{cfg: cfg, logf: logf, cache: map[string]cachedVerdict{}}
}

// Request is a single AI question about a screenshot.
type Request struct {
	Question  string
	ImagePath string
	Expect    string // yes | no (assert only)
	Provider  string // per-call override
	Model     string
	Timeout   time.Duration
	Retries   int
	Samples   int
}

// AssertResult is the outcome of an assert_ai call.
type AssertResult struct {
	Provider  string
	Model     string
	Prompt    string
	RawAnswer string
	Verdict   bool
	Pass      bool
	Samples   int
	Retries   int
	Err       error
}

func (e *Engine) resolve(r Request) (Adapter, Config, error) {
	cfg := e.cfg
	if r.Provider != "" {
		cfg.Provider = r.Provider
	}
	if r.Model != "" {
		cfg.Model = r.Model
	}
	if r.Timeout > 0 {
		cfg.Timeout = r.Timeout
	}
	if r.Retries > 0 {
		cfg.Retries = r.Retries
	}
	if r.Samples > 0 {
		cfg.Samples = r.Samples
	}
	adapter, ok := NewAdapter(cfg.Provider)
	if !ok {
		return nil, cfg, fmt.Errorf("unknown AI provider %q", cfg.Provider)
	}
	return adapter, cfg, nil
}

// AssertAI evaluates a yes/no assertion.
func (e *Engine) AssertAI(ctx context.Context, r Request) AssertResult {
	adapter, cfg, err := e.resolve(r)
	if err != nil {
		return AssertResult{Err: err}
	}
	prompt := buildAssertPrompt(r.Question, r.ImagePath)
	res := AssertResult{Provider: cfg.Provider, Model: cfg.Model, Prompt: prompt, Samples: cfg.Samples}

	verdicts := make([]bool, 0, cfg.Samples)
	var lastRaw string
	for s := 0; s < cfg.Samples; s++ {
		raw, v, retries, err := e.invokeWithRetry(ctx, adapter, cfg, prompt, r.ImagePath)
		res.Retries += retries
		if err != nil {
			res.Err = err
			res.RawAnswer = raw
			return res
		}
		lastRaw = raw
		verdicts = append(verdicts, v)
	}

	res.RawAnswer = lastRaw
	verdict, ok := majority(verdicts)
	if !ok {
		res.Err = fmt.Errorf("no majority verdict across %d samples", cfg.Samples)
		return res
	}
	res.Verdict = verdict
	res.Pass = (verdict == expectBool(r.Expect))
	return res
}

// ReadText extracts a free-form value from a screenshot (read_text_ai).
func (e *Engine) ReadText(ctx context.Context, r Request) (value, raw string, err error) {
	adapter, cfg, err := e.resolve(r)
	if err != nil {
		return "", "", err
	}
	prompt := buildExtractPrompt(r.Question, r.ImagePath)
	out, err := e.runWithTimeout(ctx, adapter, cfg, prompt, r.ImagePath)
	if err != nil {
		return "", out, err
	}
	return firstMeaningfulLine(out), out, nil
}

// invokeWithRetry runs one sample, retrying transient/unparseable failures.
func (e *Engine) invokeWithRetry(ctx context.Context, adapter Adapter, cfg Config, prompt, image string) (raw string, verdict bool, retries int, err error) {
	key := e.cacheKey(cfg.Provider, prompt, image)
	if c, ok := e.cacheGet(key); ok {
		return c.raw, c.verdict, 0, c.err
	}

	attempts := cfg.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			retries++
			backoff := time.Duration(attempt) * 500 * time.Millisecond
			e.logf("debug", fmt.Sprintf("AI retry %d after %v: %v", attempt, backoff, lastErr))
			select {
			case <-ctx.Done():
				return "", false, retries, ctx.Err()
			case <-time.After(backoff):
			}
		}
		out, rerr := e.runWithTimeout(ctx, adapter, cfg, prompt, image)
		if rerr != nil {
			lastErr = rerr
			continue
		}
		v, perr := ParseVerdict(out)
		if perr != nil {
			lastErr = perr
			raw = out
			continue
		}
		e.cachePut(key, cachedVerdict{raw: out, verdict: v})
		return out, v, retries, nil
	}
	return raw, false, retries, lastErr
}

// runWithTimeout invokes the provider subprocess with a hard timeout.
func (e *Engine) runWithTimeout(ctx context.Context, adapter Adapter, cfg Config, prompt, image string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	cmd := adapter.BuildCommand(cctx, prompt, image, cfg.Model)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		return stdout.String(), fmt.Errorf("AI call timed out after %v", cfg.Timeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s invocation failed: %s", adapter.Name(), msg)
	}
	return stdout.String(), nil
}

func (e *Engine) cacheKey(provider, prompt, image string) string {
	h := sha256.New()
	if data, err := os.ReadFile(image); err == nil {
		h.Write(data)
	}
	h.Write([]byte("\x00" + provider + "\x00" + prompt))
	return fmt.Sprintf("%x", h.Sum(nil))
}

func (e *Engine) cacheGet(key string) (cachedVerdict, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.cache[key]
	return c, ok
}

func (e *Engine) cachePut(key string, c cachedVerdict) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cache[key] = c
}

func expectBool(expect string) bool {
	switch strings.ToLower(strings.TrimSpace(expect)) {
	case "no", "false":
		return false
	default: // "", yes, true
		return true
	}
}

func buildAssertPrompt(question, imagePath string) string {
	return fmt.Sprintf(`%s

You are looking at a screenshot saved at this path: %s
Open and view that image file, then examine it carefully to answer the question.

Answer with a single word on the first line: YES or NO.
Then optionally add one short sentence explaining why.`, strings.TrimSpace(question), imagePath)
}

func buildExtractPrompt(question, imagePath string) string {
	return fmt.Sprintf(`%s

You are looking at a screenshot saved at this path: %s
Open and view that image file, then answer based only on what is visible.
Reply with only the answer value on the first line, with no extra words.`, strings.TrimSpace(question), imagePath)
}
