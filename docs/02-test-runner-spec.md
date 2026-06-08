# 02 — Test Runner: Core Engine & CLI (`uitest`)

This spec covers the **Runner Core** (the Go engine that loads a
[Test Session](03-testsession-yaml-spec.md), launches the app under test, drives it like a human,
and verifies results by asking an AI agent about screenshots) and the **CLI front-end**
(`uitest`) that exposes it on the command line.

The GUI front-end (`uitest-gui`) is a separate, thin shell over the same core; see
[04 — GUI Runner Spec](04-gui-runner-spec.md). The HTML report both front-ends produce is
specified in [05 — Report Spec](05-report-spec.md).

> **One core, two front-ends.** Everything in §2–§10 (lifecycle, command catalog, AI engine,
> targets, variables, artifacts) belongs to the shared core and is identical regardless of how the
> run was started. Only §1 (the CLI) is CLI-specific.

---

## 1. Command-line interface (`uitest`)

```
uitest run <session.yaml> [options]
```

| Option | Default | Description |
| --- | --- | --- |
| `<session.yaml>` | (required) | Path to the Test Session file to execute. |
| `--out <dir>` | `./test-results/<timestamp>` | Output directory for artifacts (results, screenshots, logs). |
| `--ai <provider>` | from session, else `claude` | Override AI provider: `claude` \| `codex` \| `cursor`. |
| `--filter <glob>` | (all) | Run only test cases whose `id` or `tags` match. |
| `--fail-fast` | from session, else `false` | Stop on first failed case. |
| `--dry-run` | `false` | Validate + print the resolved plan without launching the app or acting. |
| `--headed / --headless` | `--headed` | Whether a real interactive desktop is expected. |
| `--timeout-scale <f>` | `1.0` | Multiply all timeouts (useful on slow CI). |
| `--log-level <level>` | `info` | `error` \| `warn` \| `info` \| `debug` \| `trace`. |
| `--no-app-launch` | `false` | Attach to an already-running app instead of launching it. |
| `--report-embed` | `false` | Inline CSS/JS and base64 screenshots into a single self-contained `report.html`. |
| `--open` | `false` | Open `report.html` in the default browser when the run finishes. |

Auxiliary subcommands:

| Command | Description |
| --- | --- |
| `uitest validate <session.yaml>` | Parse + schema-validate only; exit non-zero on error. |
| `uitest list <session.yaml>` | Print the test cases / steps the runner would execute. |
| `uitest doctor` | Check the environment: AI CLIs reachable, screen capture works, input permissions. |

### Exit codes

| Code | Meaning |
| --- | --- |
| `0` | All non-skipped cases passed. |
| `1` | One or more cases failed (assertion returned negative). |
| `2` | Runner/setup error (bad YAML, app failed to launch, AI provider unreachable). |
| `3` | Aborted (e.g. interrupted, timeout at session level). |

---

## 2. Runner lifecycle

1. **Load & validate** the session file; resolve defaults and CLI overrides.
2. **Doctor checks** (fast): confirm the selected AI provider CLI is callable and screen capture
   works. Fail with exit `2` if not.
3. **Launch app** (unless `--no-app-launch`): start the configured process, then wait until the
   `readyWhen` condition is satisfied or `startupTimeout` elapses.
   - Then run `session.setup` once (best-effort bootstrap; failures are logged, not fatal).
4. **For each test case** (respecting `--filter` and order):
   1. Run `session.beforeEach` (best-effort; never changes the verdict).
   2. Run case-level `setup` steps.
   3. Run each `step` in order — execute its `machine` command(s). This is the **act** phase.
   4. Run the case `validation`: evaluate each entry in the `assert` list. This is the **check**
      phase that decides pass/fail.
   5. Run case-level `teardown` steps **always**, even after failure (legacy name).
   6. Run case-level `cleanup` steps **always**, even after failure — dismiss modals,
      close forms, reset navigation so the next case starts from a clean state.
      The runner binds and activates the correct window before every click or keystroke
      in these phases (independent of `focusGuard`).
   7. Run `session.afterEach` (best-effort; never changes the verdict).
   8. If the case failed and `settings.recoverOnCaseFailure` is `true` (and the runner
      launched the app): **force-kill** the app, **relaunch**, run `session.recoverSteps`
      (strict — e.g. log in again), run `session.beforeEach` (reposition/resize), then
      **retry the case once**. The failure is recorded only if that retry also fails.
   9. Record the case result.
5. **Shut down** the app gracefully (configurable: close window, then kill if it lingers).
6. **Report**: write `results.json` + `summary.txt`/`summary.md`, set exit code.

### Failure & control flow

- A failed **machine** command → step fails. A failed **step** → case fails, remaining steps
  skipped (unless the step has `continueOnFailure: true`).
- During validation, a failed **assert** entry fails the case. A case **passes only if every**
  `assert` entry passes.
- `retries` (per command, step, or case) re-run the unit before declaring failure.
- `failFast` stops the session at the first failed case.
- Any **actuation/observation error** (e.g., window not found, capture failed) is a step `error`,
  treated like a failure for exit-code purposes but tagged distinctly in the report.

---

## 3. Command catalog

A step's `machine` section runs one or more of these commands; `validation.assert` entries use
the observation commands. Commands fall into three families.

### 3.1 Actuation — mouse

| Action | Args | Notes |
| --- | --- | --- |
| `mouse_move` | `target` | Move pointer to a point (see [Targets](#5-targets--coordinates)). |
| `mouse_click` | `target`, `button=left`, `count=1` | Click/double-click at a point. |
| `mouse_down` | `target`, `button=left` | Press and hold (pair with `mouse_up`). |
| `mouse_up` | `target`, `button=left` | Release. |
| `mouse_drag` | `from`, `to`, `button=left` | Press at `from`, move to `to`, release. |
| `mouse_scroll` | `target`, `dx=0`, `dy` | Scroll wheel by ticks; positive `dy` scrolls down. |

`button` ∈ `left | right | middle`.

### 3.2 Actuation — keyboard

| Action | Args | Notes |
| --- | --- | --- |
| `type_text` | `text`, `perCharDelayMs=0` | Type a literal string. |
| `key_press` | `keys` | A chord, e.g. `"Ctrl+S"`, `"Alt+F4"`, `"Enter"`. Supports sequences `["Ctrl+A","Delete"]`. |
| `key_down` | `key` | Hold a key. |
| `key_up` | `key` | Release a key. |

Key names follow a documented table (`Enter`, `Tab`, `Esc`, `F1`..`F12`, `Up/Down/Left/Right`,
`Ctrl/Alt/Shift/Win`, printable characters).

### 3.3 Actuation — application & window

| Action | Args | Notes |
| --- | --- | --- |
| `launch_app` | `path`, `args`, `workingDir`, `readyWhen`, `timeout` | Usually implicit from the session, but can be invoked mid-test. |
| `focus_window` | `window` | Bring a window to the foreground. |
| `close_window` | `window` | Send a close request. |
| `move_window` | `window`, `x`, `y` | Reposition. |
| `resize_window` | `window`, `width`, `height` | Resize. |
| `wait` | `ms` **or** `forAI` | Sleep a fixed time, or poll an AI question until true/timeout. |

### 3.4 Observation

| Action | Args | Notes |
| --- | --- | --- |
| `screenshot` | `target=screen`, `save` | Capture screen/window/region; optionally persist as a named artifact. Pure capture, no verdict. |
| `assert_ai` | `question`, `target`, `expect=yes`, `provider?`, `retries?` | **The assertion.** Capture target, ask the AI the question, parse yes/no, compare to `expect`. |
| `read_text_ai` | `question`, `target`, `store` | Ask the AI to extract a value (e.g., a displayed number) and store it in a variable for later steps/assertions. |

### 3.5 Synchronization & self-correction (closed-loop actions)

The biggest source of flakiness in input-driven UI testing is **timing**: if the app is busy and
the first click doesn't register (or a dialog opens late), every following action lands on the
wrong target. To prevent that cascade, any **actuation** command may be wrapped with a
cost-ordered loop — *wait-until-ready → act → did-it-work → retry just this action* — escalating
to the AI only when cheap checks can't decide.

| Field | Applies to | Meaning |
| --- | --- | --- |
| `waitBefore` | any actuation | A **condition** that must hold before acting (poll until true or timeout). |
| `verify` | any actuation | A **condition** that must hold *after* acting; if not, the action is retried. |
| `actionRetries` | any actuation | Re-attempts of this action when `verify` fails (default `settings.defaultActionRetries`). |
| `uia` | mouse/keyboard | Locate the target via the UI Automation tree (Phase 2). |
| `find` | mouse/keyboard | Locate the target by natural-language AI search (Phase 3). |

A **condition** (used by `waitBefore`, `verify`, and `wait.forAI`) carries one or more *rungs*,
evaluated cheapest-first; all present rungs must hold:

| Rung | Cost | Meaning |
| --- | --- | --- |
| `window: { title\|process\|class, gone }` | free | A window is present (or absent, with `gone: true`). |
| `stable: true` | free | The target region has stopped changing (animations/spinners done). |
| `changed: true` | free | The target region changed since the action (it registered). |
| `uia: { automationId\|name\|controlType, state, value }` | free | A control's state (Phase 2). |
| `question: "..."` (+ `expect`) | AI call | An AI vision yes/no — used when it's the declared check, or as escalation. |

**Defaults (cost-minimizing).** With `settings.autoSettle` on (default), the runner waits for the
target to be visually **stable** before each action and, for `mouse_click`/`mouse_drag`/
`mouse_scroll`, auto-verifies that the click **changed** something — re-clicking up to
`defaultActionRetries` if not. Typing and key chords are **never** auto-retried (re-sending would
duplicate input); add an explicit `verify` if you want them gated. The AI is invoked only for
conditions that carry a `question`, or — when `settings.aiEscalation` is on — once at the end to
**diagnose** why an action never took effect (the diagnosis is recorded in the report).

```yaml
- human: "Click Save and confirm the Save dialog opens"
  machine:
    action: mouse_click
    target: { x: 412, y: 88 }
    waitBefore: { stable: true }                 # don't click mid-animation
    verify:     { window: { title: "Save As" } } # re-click until the dialog appears
    actionRetries: 3
```

---

## 4. The AI assertion engine

This is the heart of verification. `assert_ai` (and `read_text_ai`, `wait.forAI`) all route
through it.

### 4.1 Flow

```
assert_ai(question, target, expect)
   1. capture screenshot of `target`            -> image file (artifact)
   2. build prompt (question + answer contract) -> prompt string
   3. invoke provider CLI with image + prompt   -> raw stdout
   4. parse verdict (yes/no/true/false)         -> boolean
   5. compare to `expect`                        -> pass / fail
   6. record evidence (image, prompt, raw, parsed, verdict)
```

### 4.2 Provider invocation

The provider is a subprocess. The runner passes the **question** and the **screenshot path** and
reads the answer from stdout. Conceptual mapping:

| Provider | Invocation (conceptual) |
| --- | --- |
| `claude` | `claude -p "<prompt incl. image reference>"` |
| `codex`  | `codex exec "<prompt incl. image reference>"` |
| `cursor` | `cursor-agent -p "<prompt incl. image reference>"` (non-interactive) — included if/when the Cursor CLI supports headless prompting; otherwise this provider is a no-op until available. |

> **Note on images.** Exact image-passing differs per CLI (path argument, stdin, or an embedded
> reference in the prompt). The engine isolates this behind a small **provider adapter**
> interface so new providers can be added without touching test logic. Each adapter declares: how
> it receives the image, how it receives the question, and how its stdout maps to a verdict.

Provider configuration is resolved in this precedence: `--ai` CLI flag → step `provider` →
session `ai.provider` → built-in default (`claude`).

### 4.3 The answer contract (prompt construction)

To make parsing reliable, the engine appends a strict answer contract to every question, e.g.:

```
<user question>

Answer with a single word on the first line: YES or NO.
Then optionally add one short sentence explaining why.
```

### 4.4 Verdict parsing

- Normalize stdout: trim, lowercase, take the first token / first line.
- Map `yes` / `true` / `pass` / `1` → **true**; `no` / `false` / `fail` / `0` → **false**.
- `expect` (default `yes`) defines which boolean means **pass**:
  - `expect: yes` → verdict `true` ⇒ pass.
  - `expect: no`  → verdict `false` ⇒ pass.
- **Unparseable / ambiguous** answer → step `error` (not a silent pass). The engine may
  `retries` and, optionally, use **best-of-N majority vote** for flaky models.

### 4.5 Reliability controls

- **Retries** with backoff for transient CLI/network failures.
- **Majority vote** (optional `samples: N`) to reduce model nondeterminism.
- **Caching** (optional): identical (image-hash, prompt, provider) → reuse the prior verdict
  within a run to save cost/time.
- **Timeouts**: each AI call has a hard timeout; exceeding it is a step `error`.
- **Evidence**: every call stores prompt, raw answer, parsed verdict, and the image, so failures
  are auditable.

---

## 5. Targets & coordinates

A **target** specifies where an action applies or what to capture.

```yaml
# A point (for mouse actions)
target: { x: 120, y: 64 }

# A window (for focus/close/screenshot)
target: { window: "Untitled - Notepad" }

# A rectangle (for screenshot/assert_ai), relative to a window or the screen
target: { rect: { x: 0, y: 0, width: 400, height: 200 }, relativeTo: "window" }

# The whole screen
target: screen
```

- **Coordinate spaces:** `relativeTo` ∈ `window` (default for app actions) | `screen`. The runner
  is **DPI-aware**; coordinates are logical pixels unless `raw: true`.
- **Window matching:** a `window` may be matched by `title` (substring/regex), `process`, or
  `class`. Matching strategy and ambiguity handling are defined in the session settings.
- Mouse actions require a **point**; `screenshot`/`assert_ai` accept point-free targets
  (`window`, `rect`, `screen`).

---

## 6. Variables & data

- `read_text_ai ... store: <name>` saves a value into the session variable bag.
- Steps can interpolate variables in `text`, `question`, and `target` via `${name}`.
- Built-in variables: `${session.outDir}`, `${case.id}`, `${step.index}`, `${timestamp}`.

---

## 7. Artifacts & reporting

Written under `--out`:

```
test-results/<timestamp>/
├─ report.html           # PRIMARY human-facing report (see 05-report-spec.md)
├─ results.json          # machine-readable: per case/step status, timings, evidence refs
├─ run.log               # full execution log at the chosen log level
├─ assets/               # report css/js (omitted when --report-embed makes report.html standalone)
└─ screenshots/
   ├─ TC-001_step-01.png         # captures taken during steps
   ├─ TC-001_assert-00_actual.png    # "what actually is" for an assertion
   ├─ TC-001_assert-00_expected.png  # "what should be" (baseline), when provided
   └─ ...
```

The **`report.html`** is the primary deliverable: a self-contained HTML report that shows, for
every assertion, the **expected ("what should be")** and **actual ("what actually is")**
screenshots side by side. Its full specification — layout, contents, and the
expected/actual rule — is in [05 — Report Spec](05-report-spec.md).

### `results.json` shape (sketch)

```json
{
  "session": "Login smoke",
  "startedAt": "2026-06-04T13:12:00Z",
  "finishedAt": "2026-06-04T13:13:20Z",
  "provider": "claude",
  "summary": { "total": 3, "passed": 2, "failed": 1, "skipped": 0, "errors": 0 },
  "cases": [
    {
      "id": "TC-001",
      "name": "User can log in",
      "status": "passed",
      "durationMs": 8421,
      "steps": [
        {
          "index": 0,
          "human": "Type the username and password, then click Sign in",
          "machine": [
            { "action": "type_text", "status": "passed" },
            { "action": "mouse_click", "status": "passed" }
          ],
          "status": "passed"
        }
      ],
      "validation": {
        "human": "The dashboard is visible and no error banner is shown.",
        "status": "passed",
        "assert": [
          {
            "human": "Dashboard visible",
            "action": "assert_ai",
            "status": "passed",
            "evidence": {
              "screenshot": "screenshots/TC-001_assert-00.png",
              "question": "Is a dashboard with a welcome message visible?",
              "expect": "yes",
              "rawAnswer": "YES. The dashboard greets the user.",
              "verdict": true
            }
          }
        ]
      }
    }
  ]
}
```

---

## 8. Configuration precedence

For any setting (provider, timeouts, output dir, failFast):

```
CLI flag  >  per-step value  >  per-case value  >  session settings  >  built-in default
```

---

## 9. Error handling & safety

- **App fails to launch / `readyWhen` times out** → exit `2`, no cases run.
- **Window not found** for an action → step `error`; case fails; teardown still runs.
- **AI provider unreachable / times out** → step `error` after retries.
- **Interrupt (Ctrl-C)** → attempt graceful app shutdown, flush artifacts, exit `3`.
- **Guardrails:** the runner only acts within the test window; a global "panic" key combination
  and a max-session-duration abort prevent a runaway test from holding the desktop hostage.

---

## 10. Extensibility

- **Provider adapters** (§4.2) are pluggable; adding `gemini`, a local model, etc. requires only
  a new adapter.
- **Custom actions** can be registered (e.g., `paste_file`, `assert_pixel`) without changing the
  YAML schema beyond the `action` name.
- The schema is **versioned** (`version:` in the session) so the runner can evolve compatibly.
