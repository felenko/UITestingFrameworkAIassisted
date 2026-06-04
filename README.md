# UI Testing Framework

![AI-Assisted UI Testing — drives apps like a human, verifies with AI](social-ai-ui-testing.png)

An AI-assisted UI test framework that drives a real desktop application **the same way a
human user would** — moving the mouse, clicking, dragging, and typing — and then **verifies
results by asking an AI agent questions about what is on screen**.

Instead of brittle selectors or accessibility-tree scraping, assertions are expressed as
plain-English questions ("Is the login dialog showing an error message?"). The runner takes a
screenshot (whole screen, a window, or a rectangle) and forwards the image plus the question to
an AI agent (`claude`, `codex`, or `cursor`). The agent answers, the runner parses a simple
`yes`/`no` (or `true`/`false`), and that becomes a **pass** or **fail**.

## Why this exists

Traditional UI automation breaks when a control's ID changes, a layout shifts, or an app has no
clean accessibility tree. This framework takes the opposite approach:

- **Test like a real user.** Actions are physical input (mouse + keyboard via the OS), so you
  exercise the app through exactly the path a person takes — no private hooks, no app
  instrumentation required.
- **Assert by perception, not selectors.** Validation is "does the screen look right?" answered
  by an AI vision agent, which is resilient to cosmetic/layout changes and reads dialogs, toasts,
  charts, and images the way a human reviewer would.
- **Readable by everyone.** Each step carries a plain-English `human` description next to the
  `machine` commands, so QA, devs, and stakeholders can review the same file.
- **Auditable results.** Every run emits an HTML report with **expected-vs-actual screenshots**
  for each check, so a pass/fail is always backed by visual evidence.

## How it works

A run flows through three stages (see the diagram above), repeated per step:

1. **Interact** — the runner performs real OS-level input (move, click, drag, type, key combos)
   to drive the app like a human.
2. **Capture** — it screenshots the whole screen, a specific window, or a rectangle.
3. **Verify with AI** — the screenshot + a plain-English question go to an AI agent, whose
   `yes`/`no` answer becomes a **pass** or **fail** (with optional retries + majority vote).

## Runners

Built in **Go** as **two front-ends over one shared Runner Core**:

- **`uitest` — CLI runner** for scripting/CI ([spec](docs/02-test-runner-spec.md)).
- **`uitest-gui` — GUI runner** built with [`webview/webview_go`](https://github.com/webview/webview_go),
  to pick a session, watch it run live, and review results ([spec](docs/04-gui-runner-spec.md)).

Every run produces a self-contained **`report.html`** that shows, for each assertion, **what
should be** (expected) next to **what actually is** (actual) ([spec](docs/05-report-spec.md)).

## Technical details

| Area | Choice |
| --- | --- |
| Language | **Go** (single shared Runner Core; two front-ends compiled from it). |
| Input actuation | Pure-Go **Win32 `SendInput`** — real mouse/keyboard events at OS level (no app hooks). |
| Screen capture | **GDI** capture of full screen, a window by title, or a pixel rectangle. |
| Assertion engine | Pluggable **AI adapters** (`claude`, `codex`, `cursor`) invoked as CLI agents; built-in retries, majority vote, and response caching. |
| Test format | Human-readable **`TestSession.yaml`** — `human` intent + `machine` commands + `validation`/`assert`. |
| Reporting | Self-contained **`report.html`** + machine-readable **`results.json`**, with expected-vs-actual screenshots and a baseline-approval workflow. |
| GUI shell | [`webview/webview_go`](https://github.com/webview/webview_go) (HTML/CSS/JS UI bound to the Go core via an event bus). |
| Platform | **Windows** today; the core is structured so actuation/capture back-ends can be ported. |

> **Targeting note:** apps with a clean UI Automation tree (stable `AutomationId`s) can be driven
> far more robustly than by pixel coordinates. A real-world example (including hover-to-expand
> accordion navigation) is the **Visual Casino 8 smoke session**.
>
> **`VisualCasino8_Smoke.yaml` is intentionally NOT in this repo.** It contains environment
> credentials, so it is **gitignored** and kept with its own project at
> `C:\Source\Biometrica\VisualCasino8_experiments\visualcasino6\VisualCasino8_Smoke.yaml`.
> Treat it as the canonical large/real example when authoring new sessions.

## Documents

| Doc | Purpose |
| --- | --- |
| [docs/01-overview.md](docs/01-overview.md) | Vision, goals, glossary, high-level architecture (the "initial spec"). |
| [docs/02-test-runner-spec.md](docs/02-test-runner-spec.md) | Runner Core + CLI (`uitest`): lifecycle, command catalog, AI assertion engine. |
| [docs/03-testsession-yaml-spec.md](docs/03-testsession-yaml-spec.md) | The `TestSession.yaml` file format — human-readable schema and conventions. |
| [docs/04-gui-runner-spec.md](docs/04-gui-runner-spec.md) | GUI runner (`uitest-gui`): Go + webview, live run view, results. |
| [docs/05-report-spec.md](docs/05-report-spec.md) | HTML report: mandatory expected-vs-actual screenshots, baselines, data model. |
| [examples/TestSessionCases.yaml](examples/TestSessionCases.yaml) | A fully annotated, runnable-shaped example. |

## Structure of a test case

A **test case** is made of multiple **steps** plus a final **validation**:

- Each **step** has a **`human`** section (plain-English intent a stakeholder can approve) and a
  **`machine`** section (the literal commands the runner executes — `mouse_click`, `type_text`, …).
- The case ends with a **validation**: a `human` description of the acceptance criteria, whose
  machine equivalent is the **`assert`** section (AI yes/no checks that decide pass/fail).

```yaml
- id: TC-001
  name: "User can save the document"
  steps:
    - human: "Click the Save button on the toolbar"
      machine:
        - action: mouse_click
          target: { x: 412, y: 88 }
  validation:
    human: "A 'Save As' dialog should appear and no error is shown."
    assert:
      - action: assert_ai
        question: "Is a 'Save As' dialog visible on screen?"
        target: screen
        expect: yes
```

## Status

Both runners are **implemented** in Go and verified end-to-end on Windows:

- **`uitest` (CLI)** — `run`, `validate`, `list`, `doctor`, `approve`; pure-Go Win32 actuation
  (SendInput), GDI screen capture, the AI assertion engine (claude/codex/cursor adapters with
  retries, majority vote, caching), and the self-contained `report.html` + `results.json`.
- **`uitest-gui` (GUI)** — `webview/webview_go` shell over the same core, with a session picker,
  overview tree, live run view (streamed events, live screenshots, assertion feed, log), and
  report access.

See **[BUILD.md](BUILD.md)** to build and run. Quick start:

```powershell
./build.ps1
./bin/uitest.exe doctor
./bin/uitest.exe run notepad-demo.yaml --open
```

> The GUI front-end requires a C toolchain (CGO) and the WebView2 runtime; the CLI does not.
