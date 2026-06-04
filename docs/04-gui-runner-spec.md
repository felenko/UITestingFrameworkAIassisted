# 04 — GUI Runner Specification (`uitest-gui`)

`uitest-gui` is the **desktop front-end** for the framework. It is the same
[Runner Core](02-test-runner-spec.md) wrapped in a thin native window so a user can pick a
session, run it, watch it execute live, and review the [HTML report](05-report-spec.md) — without
touching a terminal.

> **Relationship to the CLI.** The GUI does **not** reimplement any runner logic. It links the
> same Go core package as `uitest` and subscribes to the core's **event bus** for live updates.
> Anything you can do on the CLI you can do here; the YAML format is identical.

---

## 1. Technology

- **Language:** Go.
- **Window/host:** [`github.com/webview/webview_go`](https://github.com/webview/webview_go) — a
  tiny cross-platform webview (WebView2 on Windows, WebKit on macOS, WebKitGTK on Linux) that
  renders an HTML/CSS/JS UI in a native window.
- **UI layer:** plain HTML/CSS/JS (no heavy framework required). The same CSS/JS components are
  shared with the generated `report.html` so the live view and the final report look consistent.
- **Bridge:** Go ↔ JS via the webview **bind** mechanism. Go exposes functions the UI calls
  (e.g. `pickSession`, `startRun`, `cancelRun`); Go pushes events to the UI by evaluating JS
  (`window.dispatchEvent(...)`) from the core's event bus.

```
+-------------------- uitest-gui (Go) --------------------+
|                                                         |
|  webview window  ── bind ──►  Go bridge handlers        |
|     (HTML/CSS/JS) ◄─ eval ──     │                      |
|                                  ▼                      |
|                          Runner Core (shared)           |
|                          └─ event bus ─► UI live updates|
+---------------------------------------------------------+
```

---

## 2. Launch & invocation

```
uitest-gui [session.yaml] [--ai <provider>] [--out <dir>]
```

- With **no argument**, opens to the session picker / welcome screen.
- With a **`session.yaml`**, loads it immediately and shows the run-ready view.
- CLI-style flags (`--ai`, `--out`, `--timeout-scale`, …) are accepted and pre-fill the run
  settings; the user can still change them in the UI before starting.
- Exit code mirrors the CLI when launched in a "run and quit" mode (see §6); in interactive mode
  the window stays open and exit code is `0` unless the app itself errors.

---

## 3. Screens

### 3.1 Session picker / welcome
- Open a `TestSession.yaml` (file dialog) or pick from recent sessions.
- On load, the core **validates** the file; validation errors are shown inline (same checks as
  `uitest validate`) and block running until fixed.

### 3.2 Session overview (pre-run)
- Header: session `name`, application `path`, AI `provider`, counts (cases / steps / asserts).
- A **tree** of test cases → steps (`human` text) → validation (`assert` `human` labels).
- **Run settings** panel: provider override, output dir, `failFast`, timeout scale, filter/tags.
- Buttons: **Run all**, **Run selected**, **Dry run** (validate + plan only).

### 3.3 Live run view (the core experience)
While the core executes, the GUI shows progress in real time, driven by event-bus events:

- **Progress bar / counters:** passed / failed / running / remaining.
- **Case list** with live status chips (pending → running → passed/failed/error).
- **Active step panel:** the current step's `human` text and the `machine` command(s) executing.
- **Live screenshot pane:** every `screenshot` / `assert_ai` capture appears the moment the core
  records it, so the user literally watches what the runner "sees".
- **Assertion feed:** for each `assert`, show the question, the AI's raw answer, the parsed
  verdict, and pass/fail — alongside the **expected vs actual** thumbnails.
- **Log tail:** the same lines written to `run.log`.
- Controls: **Pause** (after current step), **Cancel** (graceful app shutdown), **Re-run case**.

> The GUI never blocks the run waiting for input; it reflects what the headless core is doing.

### 3.4 Results view (post-run)
- Summary rollup identical to the report header.
- Per-case drill-down with the **expected/actual screenshot pairs** (see
  [05 — Report Spec](05-report-spec.md)).
- **Open report.html**, **Open output folder**, **Re-run failed**, **Export** buttons.
- The on-screen results view and `report.html` are generated from the **same template/data**, so
  what the user sees live is what the saved report contains.

---

## 4. Go ↔ JS bridge (contract sketch)

Functions Go **binds** for the UI to call:

| Bound function | Purpose |
| --- | --- |
| `pickSession()` | Open a native file dialog; returns the chosen path. |
| `loadSession(path)` | Parse + validate; returns session summary or validation errors. |
| `startRun(options)` | Begin execution with the given run settings. |
| `cancelRun()` / `pauseRun()` | Control an in-flight run. |
| `openReport()` / `openOutputDir()` | Open the generated report / folder via the OS. |

Events Go **pushes** to the UI (via the core event bus):

| Event | Payload (sketch) |
| --- | --- |
| `run.started` | session name, totals |
| `case.started` / `case.finished` | case id, status, durationMs |
| `step.started` / `step.finished` | case id, step index, human, machine summary, status |
| `screenshot.captured` | path, target, which (`step` \| `expected` \| `actual`) |
| `assert.finished` | case id, question, expect, rawAnswer, verdict, expected/actual paths |
| `run.finished` | summary counts, reportPath |

---

## 5. Behavior parity & differences vs CLI

| Concern | CLI (`uitest`) | GUI (`uitest-gui`) |
| --- | --- | --- |
| Runner core | shared | shared |
| YAML format | identical | identical |
| Validation | `uitest validate` | inline on load |
| Live feedback | log lines | live screens + screenshots |
| Report output | `report.html` + `results.json` | same files + in-window results view |
| Headless/CI | yes | no (needs a desktop/webview host) |
| Exit code semantics | always | only in run-and-quit mode (§6) |

---

## 6. Run-and-quit mode (optional CI-ish use)

```
uitest-gui run <session.yaml> --quit-on-finish [--open]
```

Runs the session with the GUI visible for observation, then **exits with the CLI exit codes**
(0/1/2/3) when finished. Useful for demos and supervised runs where a human watches but an exit
code is still wanted.

---

## 7. Out of scope (initial version)

- Editing/authoring sessions in the GUI (it loads and runs; authoring is hand-written YAML).
- Recording user actions into a session (no record-and-replay).
- Remote/headless operation of the GUI (use the CLI for CI).
