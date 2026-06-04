# 05 — Results & HTML Report Specification

The primary, human-facing output of every run is **`report.html`** — a self-contained HTML
report. Its defining feature: for **every assertion** it shows, side by side, **what should be**
(the *expected* screenshot) and **what actually is** (the *actual* screenshot captured from the
app under test).

Both runners ([CLI](02-test-runner-spec.md) and [GUI](04-gui-runner-spec.md)) emit the same
report from the same data, so the report is identical regardless of how the run was started.

---

## 1. Outputs

```
test-results/<timestamp>/
├─ report.html        # the report (this document)
├─ results.json       # machine-readable results (data source for the report)
├─ run.log            # execution log
├─ assets/            # report.css, report.js  (absent when --report-embed is used)
├─ screenshots/       # actual captures (and copied/cropped expected images)
└─ baselines/         # approved "expected" images, keyed by case + assertion id
```

- With **`--report-embed`**, CSS/JS are inlined and screenshots are base64-embedded so
  `report.html` is a single portable file (e.g. to email or attach to a CI artifact).
- `results.json` is the source of truth; `report.html` is a rendering of it.

---

## 2. The expected-vs-actual rule (mandatory)

Every assertion in the report **must** display two images:

| Panel | "What should be" (Expected) | "What actually is" (Actual) |
| --- | --- | --- |
| Source | a **baseline** image (see §3) | the screenshot captured at assertion time |
| Always present? | yes — resolved via the fallback chain in §3 | yes — captured on every assert |
| Role | reference for human review | evidence the AI judged |

> The **pass/fail verdict still comes from the AI** answering the assertion question (see
> [02 §4](02-test-runner-spec.md#4-the-ai-assertion-engine)). Expected-vs-actual images are for
> **human review and auditing**, not for pixel-matching. (Pixel/diff matching remains a non-goal.)

If an assertion's screenshots are missing, the report renders an explicit **"screenshot missing"**
placeholder and the assertion is flagged — the report never silently omits an image.

---

## 3. Where the "Expected" image comes from (resolution order)

For each assertion the runner resolves the expected image in this order:

1. **Explicit `baseline`** declared on the assertion in the YAML (`baseline: path/to/expected.png`).
2. **Approved baseline** in `baselines/<case-id>/<assert-id>.png` from a prior approved run
   (the "golden image" workflow).
3. **First-run candidate:** if neither exists, the runner uses the **actual** capture as a
   *candidate* expected image, tags it **"no approved baseline yet"**, and offers to promote it
   (see §6). This guarantees the Expected panel always has an image while making it obvious the
   baseline is unverified.

The chosen source is recorded per assertion (`expected.source` = `declared | approved | candidate`).

---

## 4. Report structure

### 4.1 Header (run summary)
- Session name, application under test, AI provider/model, start/end time, total duration.
- Result counts: **passed / failed / errors / skipped**, with an overall PASS/FAIL banner.
- Environment: OS, screen resolution/DPI, runner version, and which front-end produced it.

### 4.2 Case list
- One collapsible row per test case: id, name, status chip, duration, tag badges.
- Failed/errored cases are expanded by default and sorted to the top (configurable).

### 4.3 Case detail
For each case:
- **Description** and tags.
- **Steps timeline** — each step's `human` text, the `machine` command(s) run, status, duration,
  and any step screenshots captured.
- **Validation** — the `human` acceptance criteria, then each `assert` entry rendered as an
  **assertion card**.

### 4.4 Assertion card (the heart of the report)
```
┌───────────────────────────────────────────────────────────────────┐
│ [PASS]  "Greeting is visible"                                       │
│ Question: Does the editor contain "Hello, world!"?   expect: yes    │
│ AI (claude): "YES. The greeting text is shown."      verdict: true  │
│ Target: window "Notepad"            captured: 2026-06-04T13:12:03Z   │
├──────────────────────────────┬──────────────────────────────────────┤
│  WHAT SHOULD BE (expected)   │   WHAT ACTUALLY IS (actual)           │
│  [ baseline screenshot ]     │   [ captured screenshot ]             │
│  source: approved            │   screenshots/TC-001_assert-00_actual │
└──────────────────────────────┴──────────────────────────────────────┘
```
Each card contains:
- Verdict chip (PASS/FAIL/ERROR), the optional assert `human` label.
- The **question**, the **`expect`** value, the AI's **raw answer**, and the **parsed verdict**.
- Provider/model used, target, capture timestamp, and retry/sample info if applicable.
- The **expected** and **actual** images side by side, each clickable to open full-size
  (lightbox). A subtle **toggle/overlay** (swipe or opacity slider) is offered to compare them,
  clearly labeled "visual aid only — verdict is from the AI".
- The expected panel shows its **source badge** (`declared` / `approved` / `candidate`).

---

## 5. `results.json` (data model the report renders)

```json
{
  "session": "Notepad smoke test",
  "frontend": "cli",
  "runnerVersion": "0.1.0",
  "environment": { "os": "windows", "screen": "1920x1080@1.0dpi" },
  "startedAt": "2026-06-04T13:12:00Z",
  "finishedAt": "2026-06-04T13:13:20Z",
  "provider": "claude",
  "summary": { "total": 3, "passed": 2, "failed": 1, "errors": 0, "skipped": 0 },
  "cases": [
    {
      "id": "TC-001",
      "name": "User can type text and see it in the editor",
      "status": "passed",
      "durationMs": 8421,
      "steps": [
        {
          "index": 0,
          "human": "Type the configured greeting into the editor",
          "machine": [ { "action": "type_text", "status": "passed" } ],
          "screenshots": [],
          "status": "passed"
        }
      ],
      "validation": {
        "human": "The greeting is visible and no error dialog is shown.",
        "status": "passed",
        "assert": [
          {
            "id": "assert-00",
            "human": "Greeting is visible",
            "action": "assert_ai",
            "question": "Does the editor contain 'Hello, world!'?",
            "expect": "yes",
            "provider": "claude",
            "rawAnswer": "YES. The greeting text is shown.",
            "verdict": true,
            "status": "passed",
            "expected": {
              "source": "approved",
              "image": "baselines/TC-001/assert-00.png"
            },
            "actual": {
              "image": "screenshots/TC-001_assert-00_actual.png",
              "capturedAt": "2026-06-04T13:12:03Z"
            }
          }
        ]
      }
    }
  ]
}
```

Every `assert` object **always** carries an `expected` and an `actual` block (the report depends
on this invariant).

---

## 6. Baseline approval workflow

To support the "what should be" panel meaningfully, baselines can be created/updated:

- **CLI:** `uitest approve <results-dir> [--case TC-001] [--assert assert-00] [--all]`
  promotes the recorded actual/candidate image(s) into `baselines/<case>/<assert>.png`.
- **GUI:** each assertion card in the results view has an **"Approve as baseline"** button
  (and "Approve all passed"), which does the same promotion.
- Baselines are intended to be **committed to source control** alongside the session so future
  runs compare against a reviewed reference.
- A run where any assertion used a `candidate` expected (no approved baseline) is flagged in the
  header as **"contains unverified baselines"**.

---

## 7. Accessibility & portability

- Report is responsive and readable at typical laptop widths; images lazy-load.
- Color is never the only signal — status uses text labels + icons in addition to color.
- `--report-embed` produces a single self-contained `.html` with no external dependencies,
  suitable for CI artifact upload or emailing.
