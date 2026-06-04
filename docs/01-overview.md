# 01 — Overview & Vision (Initial Spec)

> This document captures the original idea verbatim-in-spirit and turns it into a stable
> reference. It is the "north star" the other specs elaborate on.

## 1. The idea (as proposed)

1. The **UI Test Executer** is a **separate executable**. It is fed a `TestSessionCases.yaml`
   file, then **starts the application** specified in that session file and **executes the test
   cases**.
2. The YAML file holds **two representations** for each test case:
   - **Human readable** — the test described in plain English, as multiple steps.
   - **Machine commands** — the actual commands executed by the runner.
3. **Core idea:** the runner interacts with the computer **just like a user** — commands can be
   mouse move, mouse click, mouse drag, type from keyboard, etc.
4. **Seeing the screen:** a separate concern is how the runner "sees" windows to check results.
   The runner can take a screenshot of the **whole screen**, a **window**, or a **rectangle**
   given by coordinates, and then **ask an AI agent to analyze it**.
5. **Assertions via AI:** an assertion is implemented as a command that asks an AI agent of our
   choosing a question and parses a simple `Yes/No` (`true/false`) answer into **passed/failed**.
   The agent is invoked as a subprocess, e.g.:
   - `claude -p "question"`
   - `codex exec "question"`
   - and, if available, the equivalent Cursor CLI option.

## 2. Why this approach

- **User-realistic.** Driving real input devices exercises the application through the exact same
  path a human uses — including focus, z-order, animations, and timing — catching defects that
  API-level or accessibility-tree tests miss.
- **Resilient assertions.** Asking "does this look right?" in natural language survives cosmetic
  refactors (renamed controls, restyled widgets) that would break selector-based checks.
- **Readable by everyone.** The plain-English layer lets product/QA stakeholders review and sign
  off on test intent without reading code.
- **Tool-agnostic verification.** The AI provider is pluggable; the same test can be validated by
  Claude, Codex, or Cursor.

## 3. Glossary

| Term | Meaning |
| --- | --- |
| **Test Runner / Executer** | The standalone executable that loads a session, launches the app, and runs cases. |
| **Test Session** | One `TestSession.yaml` (a.k.a. `TestSessionCases.yaml`) file: app under test + settings + an ordered list of test cases. |
| **Test Case** | A named scenario containing ordered **steps** plus a final **validation**. |
| **Step** | A unit with a `human` section (plain English) and a `machine` section (one or more commands). |
| **Validation** | The pass/fail decision at the end of a case: a `human` description + a machine `assert` list. |
| **Action / Command** | The literal operation the runner performs (`mouse_click`, `type_text`, `assert_ai`, …). |
| **Actuation command** | A command that *acts* on the system (mouse/keyboard/window/app/wait). |
| **Observation command** | A command that *reads* the system (`screenshot`, `assert_ai`, `read_text_ai`). |
| **AI Provider** | The external agent CLI used to answer questions about screenshots (`claude`/`codex`/`cursor`). |
| **Assertion** | An entry in the `assert` section; an observation that yields pass/fail, typically `assert_ai`. |
| **Target** | Where an action applies: screen coordinates, a window, or a rectangle. |
| **Artifact** | Any file the runner produces: screenshots, logs, the results report. |

## 4. High-level architecture

The framework is built in **Go** and ships as **two front-ends over one shared engine**:

- **`uitest` — CLI runner.** Headless/scriptable, ideal for CI and quick local runs. See
  [02 — Test Runner Spec](02-test-runner-spec.md).
- **`uitest-gui` — GUI runner.** A desktop app built with **[webview/webview_go](https://github.com/webview/webview_go)**
  (a thin native webview hosting an HTML/JS UI driven by the Go core). Lets a user pick a session,
  watch cases run live, see screenshots as they are captured, and open the HTML report. See
  [04 — GUI Runner Spec](04-gui-runner-spec.md).

Both front-ends link the **same Runner Core** package, so a session behaves identically whether
run from the terminal or the GUI.

```
                                +-------------------------------+
   uitest        (CLI) ───────► |        Runner Core (Go)       |
   uitest-gui    (GUI/webview) ►|                               |
                                |  1. Parser & validator        |
        TestSession.yaml  ────► |  2. App launcher / lifecycle  |
                                |  3. Scheduler (cases/steps)   |
        produces        ◄────── |  4. Actuator (mouse/keyboard) | ──► OS input APIs   ──► App under test
        report.html             |  5. Observer (screenshots)    | ◄── OS capture APIs ◄── screen/window
        results.json            |  6. AI assertion engine       | ──► claude | codex | cursor (subprocess)
        screenshots/            |  7. Reporter (HTML + JSON)     |
        run.log                 |  8. Event bus (progress)      | ──► live updates to CLI log / GUI
                                +-------------------------------+
```

### Component responsibilities

- **Parser & validator** — load YAML, apply defaults, validate against the schema, fail early on
  bad input.
- **App launcher / lifecycle** — start the application defined in the session, wait until ready,
  track its window(s), tear it down at the end (or on failure).
- **Scheduler** — iterate test cases and steps in order, honoring per-case setup/teardown,
  timeouts, retries, and `failFast`.
- **Actuator** — translate actuation commands into real OS-level mouse/keyboard/window events.
- **Observer** — capture screenshots scoped to screen/window/region; persist them as artifacts.
- **AI assertion engine** — build the prompt, invoke the chosen provider with the screenshot,
  parse the verdict, and record evidence (prompt, raw answer, parsed result).
- **Reporter** — emit a machine-readable `results.json` **and** a self-contained
  **`report.html`** with mandatory expected-vs-actual screenshots (see
  [05 — Report Spec](05-report-spec.md)); set process exit code.
- **Event bus** — publishes progress events (case started, step done, assert verdict, screenshot
  captured) that the CLI renders as log lines and the GUI renders live.

### Why Go + webview

- **Single static binary** per front-end, easy to distribute, cross-compiles for Windows/macOS/Linux.
- **One core, two UIs** — the GUI is just a thin webview shell calling into the same Go engine the
  CLI uses; no logic is duplicated.
- The webview hosts plain HTML/CSS/JS, so the **live GUI view and the final HTML report share
  styling and components**.

## 5. Execution model (lifecycle)

```
load+validate session
  └─ launch application, wait for ready
       └─ for each test case (in order):
            ├─ run case setup steps (if any)
            ├─ for each step (in order):           # the "act" phase
            │     ├─ read human section (intent)
            │     └─ run machine section: one or more commands (mouse/keyboard/wait/…)
            ├─ run validation (the "check" phase): # the assert section
            │     └─ for each entry in `assert`: capture → ask AI → parse yes/no → pass/fail
            ├─ run case teardown steps (always, even on failure)
            └─ record case result (passed / failed / skipped / error)
       └─ shut down application
  └─ write report.html + results.json, set exit code
```

- A failed **machine** command fails the step; a failed step fails the case (unless marked
  `continueOnFailure`).
- The case **passes only if every entry in `validation.assert` passes**.
- `failFast: true` stops the whole session at the first failed case.
- The runner exit code is `0` only when every non-skipped case passed.

## 6. Non-goals (for the initial version)

- No **record-and-replay authoring** — tests are written by hand in YAML. (The `uitest-gui`
  front-end *runs* and *visualizes* sessions; it is not a recorder.)
- No cross-machine / grid distribution.
- No built-in image-diff/pixel-matching assertions — verification is delegated to the AI agent.
- No mobile or web-driver integration — this targets **desktop applications** on the local OS.

## 7. Technology decisions (settled)

- **Language:** Go (single static binary per front-end; easy cross-compilation).
- **Front-ends:** `uitest` (CLI) and `uitest-gui` (GUI via `webview/webview_go`), both over one
  shared Runner Core package.
- **Report:** self-contained HTML (`report.html`) plus machine-readable `results.json`.

## 8. Open questions to resolve during design

- **Coordinate system:** absolute screen pixels vs. window-relative vs. logical (DPI-scaled)
  coordinates. (Proposed default in spec 03: window-relative, DPI-aware.)
- **Window identification:** by title, process, class name, or handle? Stability across runs.
- **AI determinism & cost:** caching identical screenshot+question pairs; bounding token spend;
  handling provider rate limits and flaky answers (retry + majority vote?).
- **Secrets:** how app credentials / API keys are supplied without living in the YAML.
- **Headless/CI:** can this run on a build agent (virtual display) or only an interactive desktop?

These are tracked further in the runner spec where relevant.
