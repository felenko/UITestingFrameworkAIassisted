# 03 — `TestSession.yaml` Format Specification

This is the file the Test Runner is fed (`TestSessionCases.yaml`). It is designed to be **read
top-to-bottom like a document**: a stakeholder should understand *what is being tested* from the
human-readable parts alone, while the runner uses the machine sections to actually execute.

> **Design principle — two faces, one source of truth.** Every step carries a `human` section
> (plain English) *and* a `machine` section (commands). The test case ends with a `validation`
> whose human description is paired with its machine equivalent, the `assert` section. The human
> and machine views live side by side so they cannot drift apart.

### Shape of a test case at a glance

```
testCase
├─ steps[]                 # ordered actions
│   ├─ human:   "..."      # what this step does, in plain English
│   └─ machine: [ ... ]    # the command(s) the runner executes
└─ validation              # how we decide pass/fail
    ├─ human:  "..."       # the acceptance criteria, in plain English
    └─ assert: [ ... ]     # machine equivalent: AI yes/no checks
```

---

## 1. Top-level structure

```yaml
version: 1                     # schema version (required)

session:                       # who/what is under test + global settings
  name: ...
  description: ...
  application: { ... }
  ai: { ... }
  settings: { ... }

variables: { ... }             # optional: reusable values, interpolated as ${name}

testCases:                     # ordered list of scenarios
  - id: ...
    name: ...
    description: ...
    tags: [ ... ]
    setup:      [ ...steps... ]  # optional, runs before steps
    steps:      [ ...steps... ]  # the scenario (each step: human + machine)
    validation: { ... }          # how pass/fail is decided (human + assert)
    teardown:   [ ...steps... ]  # optional, always runs
```

---

## 2. `session`

```yaml
session:
  name: "Notepad smoke test"
  description: >
    Verifies that Notepad launches, accepts typed text, and can save a file.

  application:
    path: "C:\\Windows\\System32\\notepad.exe"  # executable to launch
    args: []                                     # command-line arguments
    workingDir: "C:\\temp"                       # optional working directory
    startupTimeout: 10s                          # max wait for readiness
    readyWhen:                                   # how we know the app is ready
      window: { title: "Untitled - Notepad" }    #   a matching window appeared
    shutdown: graceful                           # graceful | force | leaveOpen

  ai:
    provider: claude          # claude | codex | cursor
    model: default            # optional, provider-specific
    timeout: 30s              # per-question hard timeout
    retries: 1                # transient-failure retries
    samples: 1                # >1 enables majority-vote for flaky models

  settings:
    outDir: "./test-results"  # base output dir (a timestamped subfolder is created)
    defaultStepTimeout: 15s
    failFast: false           # stop session at first failed case
    screenshotOnFailure: true # always capture screen when a step fails
    windowMatch: title        # default window match strategy: title | process | class
    coordinateSpace: window   # default relativeTo for targets: window | screen
    dpiAware: true

    # --- self-correcting actuation (see 02 §3.5) ---
    autoSettle: true          # wait for visual stability before each action; auto-verify clicks
    settleTimeout: 5s         # max wait for readiness / verify
    settleInterval: 250ms     # poll / stability sampling interval
    defaultActionRetries: 2   # re-attempts of an action when verify fails
    aiEscalation: true        # on exhausted retries, ask the AI to diagnose what's blocking

    # --- input integrity (guard against accidental user interaction) ---
    focusGuard: true          # before each input action, make the bound window foreground and
                              # detect physical user input during the action; re-assert + retry
    forceTopmost: true        # keep the bound window above non-topmost windows so nothing occludes it
```

> **Input integrity.** `focusGuard` (default `true`) re-asserts the target as the foreground
> window before every mouse/keyboard action and watches for *physical* user input during the
> action, re-asserting and retrying when a person interferes; `forceTopmost` (default `true`) pins
> the bound window above non-topmost windows so a stray window can't occlude a click. Disable both
> for **modal-heavy flows that manage focus themselves** (explicit `focus_window` steps plus owned
> dialogs such as a "Logon failed" message box): the guard can otherwise fail to foreground the
> parent window while its own modal child holds the foreground.

### Field reference — `application`

| Field | Required | Description |
| --- | --- | --- |
| `path` | yes | Executable to launch. |
| `args` | no | List of CLI arguments. |
| `workingDir` | no | Working directory for the process. |
| `startupTimeout` | no | Max time to wait for `readyWhen` (default `10s`). |
| `readyWhen` | no | Readiness condition: a `window` match, a fixed `delay`, or `forAI` question. |
| `shutdown` | no | `graceful` (close window then kill if needed), `force` (kill), `leaveOpen`. |

### Field reference — `ai`

| Field | Required | Description |
| --- | --- | --- |
| `provider` | no | `claude` \| `codex` \| `cursor`. Default `claude`. |
| `model` | no | Provider-specific model selector. |
| `timeout` | no | Per-question timeout. Default `30s`. |
| `retries` | no | Retries on transient CLI/network failure. Default `1`. |
| `samples` | no | If `>1`, ask N times and take the majority verdict. Default `1`. |

---

## 3. Test case

```yaml
testCases:
  - id: TC-001                 # stable, unique identifier
    name: "User can type and see text"
    description: >
      A short paragraph describing the scenario and its acceptance criteria,
      written for a human reviewer.
    tags: [smoke, editor]      # used by --filter
    failFast: false            # optional per-case override
    retries: 0                 # optional per-case retry of the whole case

    setup:    []               # optional pre-steps (same shape as steps)
    steps:                     # required: the ordered human+machine steps
      - human: "Focus the editor and type 'Hello'"
        machine:
          - action: focus_window
            target: { window: "Untitled - Notepad" }
          - action: type_text
            text: "Hello"
    validation:                # required: how pass/fail is decided
      human: "The text 'Hello' is visible in the editor and no error is shown."
      assert:
        - action: assert_ai
          question: "Does the editor contain the text 'Hello'?"
          target: { window: "Untitled - Notepad" }
          expect: yes
    teardown: []               # optional cleanup (always runs)
```

| Field | Required | Description |
| --- | --- | --- |
| `id` | yes | Unique, stable ID (e.g. `TC-001`). |
| `name` | yes | Short human title. |
| `description` | no | Longer plain-English explanation / acceptance criteria. |
| `folder` | no | Optional `/`-separated group path (e.g. `"Auth/Login"`) for organizing large suites. Purely organizational metadata: the runner ignores it, but the GUI renders cases as a collapsible folder tree. |
| `tags` | no | Labels for filtering. |
| `setup` / `teardown` | no | Step lists; `teardown` always runs, even after failure. |
| `steps` | yes | The ordered steps that make up the scenario (each has `human` + `machine`). |
| `validation` | yes | The pass/fail decision: a `human` description + a machine `assert` list. |

---

## 4. Step — the two-faced unit (`human` + `machine`)

Every step has a **`human`** section (plain English) and a **`machine`** section (the command(s)
the runner executes). The `machine` section may be a **single command** or a **list of commands**
that together accomplish the human-described step.

```yaml
# Single machine command
- human: "Type the word 'Hello' into the editor"
  machine:
    action: type_text
    text: "Hello"

# Multiple machine commands for one human step
- human: "Open the File menu and choose Save"
  machine:
    - action: mouse_click
      target: { x: 24, y: 40 }      # File menu
    - action: mouse_click
      target: { x: 60, y: 120 }     # Save item

# Optional per-step controls live alongside human/machine
- human: "Type the greeting"
  machine:
    action: type_text
    text: "${greeting}"
  timeout: 10s
  retries: 0
  continueOnFailure: false
```

### Step fields

| Field | Required | Description |
| --- | --- | --- |
| `human` | yes | Plain-English intent — the readable layer reviewers approve. |
| `machine` | yes | One command (mapping) **or** a list of commands the runner executes. |
| `timeout` | no | Overrides `defaultStepTimeout` for this step. |
| `retries` | no | Re-run the step before failing. |
| `continueOnFailure` | no | If true, a failed step does not abort the case. |

Each command inside `machine` is `{ action: <name>, ...args }` drawn from the
[command catalog](02-test-runner-spec.md#3-command-catalog).

> **Readability rule:** `human` should read like a line from a manual test plan ("Click Save").
> `machine` is the mechanical translation. Reviewers read `human`; the runner runs `machine`.

---

## 4a. Validation — the `assert` section

The test case ends with a **`validation`**: a human statement of the acceptance criteria and its
machine equivalent, the **`assert`** list. Every entry in `assert` is an observation command
(usually `assert_ai`) that yields pass/fail.

```yaml
validation:
  human: >
    The greeting is visible in the editor and no error dialog is shown.
  assert:
    - id: greeting-visible                         # optional stable id (keys the baseline)
      human: "Greeting is visible"                 # optional per-assert label
      action: assert_ai
      question: "Does the editor contain 'Hello, world!'?"
      target: { window: "Untitled - Notepad" }
      expect: yes
      baseline: "baselines/TC-001/greeting.png"    # optional "what should be" reference image
    - human: "No error dialog"
      action: assert_ai
      question: "Is any error dialog or red error message visible on screen?"
      target: screen
      expect: no
```

| Field | Required | Description |
| --- | --- | --- |
| `human` | yes | The acceptance criteria in plain English. |
| `assert` | yes | Ordered list of assertion commands. |

Per-assert entry fields:

| Field | Required | Description |
| --- | --- | --- |
| `action` | yes | Usually `assert_ai` (also `read_text_ai`). |
| `question` | yes (for `assert_ai`) | The yes/no question posed to the AI. |
| `target` | no | What to capture (defaults to whole screen). |
| `expect` | no | `yes` (default) or `no`. |
| `id` | no | Stable id; keys this assertion's approved baseline at `baselines/<case-id>/<id>.png`. |
| `human` | no | Short label shown on the assertion card in the report. |
| `baseline` | no | Path to the **expected ("what should be")** reference image (see below). |
| `timeout` / `retries` / `provider` | no | Per-assert overrides. |

**Pass/fail rule:** the case **passes** only if **every** entry in `assert` passes. A failed
assert fails the case.

**Expected vs actual:** the runner always captures the **actual** screenshot when evaluating an
assert. The **expected** image is resolved as `baseline:` → an approved image in `baselines/` →
(first run) the captured actual as an unverified *candidate*. Both appear side by side in
[`report.html`](05-report-spec.md). The AI still decides pass/fail; the images are for human
review. See [05 — Report Spec](05-report-spec.md).

> Validation runs after `steps` complete. Use `setup`/`steps` to *act*, and `validation.assert`
> to *check*. (You may still place mid-scenario `assert_ai` commands inside a step's `machine`
> list when a check must happen partway through — but the case verdict is decided by
> `validation.assert`.)

---

## 5. Action arguments by command

> Full semantics are in [02 — Test Runner Spec §3](02-test-runner-spec.md#3-command-catalog).
> This section shows the YAML shape of each command's arguments **as it appears inside a
> `machine` list** (or inside `assert` for observation commands).

### Mouse

```yaml
- human: "Move the pointer to the Save toolbar button"
  machine:
    action: mouse_move
    target: { x: 412, y: 88 }

- human: "Click the Save toolbar button"
  machine:
    action: mouse_click
    target: { x: 412, y: 88 }
    button: left          # left | right | middle  (default left)
    count: 1              # 2 = double-click

- human: "Drag the slider from left to right"
  machine:
    action: mouse_drag
    from: { x: 100, y: 300 }
    to:   { x: 400, y: 300 }

- human: "Scroll the document down"
  machine:
    action: mouse_scroll
    target: { window: "Untitled - Notepad" }
    dy: 5                 # positive = down
```

### Keyboard

```yaml
- human: "Type a greeting"
  machine:
    action: type_text
    text: "Hello, world!"

- human: "Save with Ctrl+S"
  machine:
    action: key_press
    keys: "Ctrl+S"

- human: "Select all then delete"
  machine:
    action: key_press
    keys: ["Ctrl+A", "Delete"]   # sequence
```

### Application & window

```yaml
- human: "Bring the editor window to the front"
  machine:
    action: focus_window
    target: { window: "Untitled - Notepad" }

- human: "Wait for the splash screen to disappear"
  machine:
    action: wait
    ms: 2000

- human: "Wait until the main grid has finished loading"
  machine:
    action: wait
    forAI:
      question: "Has the data grid finished loading (no spinner visible)?"
      target: { window: "MyApp" }
      pollEvery: 1s
      timeout: 30s
```

### Observation & assertions

Observation commands appear either inside a step's `machine` list (mid-scenario checks) or, more
commonly, inside the case's `validation.assert` list.

```yaml
# screenshot inside a step's machine list (capture for the record):
- human: "Capture the whole screen for the record"
  machine:
    action: screenshot
    target: screen
    save: "after-login.png"

# assertions inside validation.assert (decide pass/fail):
validation:
  human: "The greeting is shown and no error dialog appears."
  assert:
    - human: "Greeting visible"
      action: assert_ai
      question: "Does the editor area contain the text 'Hello, world!'?"
      target: { window: "Untitled - Notepad" }
      expect: yes            # yes (default) | no

    - human: "No error dialog"
      action: assert_ai
      question: "Is an error dialog or red error message visible anywhere on screen?"
      target: screen
      expect: no             # passes when the AI answers 'no'

# read_text_ai can run in a step's machine list to capture a value for later asserts:
- human: "Read the displayed account balance"
  machine:
    action: read_text_ai
    question: "What numeric balance is shown in the top-right corner? Reply with digits only."
    target: { rect: { x: 900, y: 0, width: 380, height: 80 }, relativeTo: screen }
    store: balance           # later steps/asserts can use ${balance}
```

---

### Synchronization fields (per command)

Any actuation command may add `waitBefore`, `verify`, and `actionRetries` to make it
self-correcting (full semantics in [02 §3.5](02-test-runner-spec.md#35-synchronization--self-correction-closed-loop-actions)):

```yaml
- human: "Open the File menu, waiting until it's actually shown"
  machine:
    action: mouse_click
    target: { x: 24, y: 40 }
    waitBefore: { stable: true }          # don't click while the UI is animating
    verify:     { changed: true }         # re-click if the menu didn't open
    actionRetries: 3

- human: "Type the filename once the Save dialog is up"
  machine:
    action: type_text
    text: "report.txt"
    waitBefore: { window: { title: "Save As" } }   # wait for the dialog before typing
```

## 6. Targets (recap)

| Form | Use | Example |
| --- | --- | --- |
| Point | mouse actions | `{ x: 120, y: 64 }` |
| Window | focus/close/screenshot/assert | `{ window: "Untitled - Notepad" }` |
| Rectangle | screenshot/assert | `{ rect: { x:0, y:0, width:400, height:200 }, relativeTo: window }` |
| Screen | full-screen capture/assert | `screen` |

- `relativeTo` defaults to `settings.coordinateSpace` (`window`).
- Coordinates are DPI-aware logical pixels unless `raw: true`.

---

## 7. Variables

```yaml
variables:
  username: "qa_user"
  greeting: "Hello, world!"

# ...used later as:
- human: "Type the configured greeting"
  machine:
    action: type_text
    text: "${greeting}"
```

- Interpolation works in `text`, `question`, `save`, and string fields of `target`.
- `read_text_ai ... store:` adds run-time variables.
- Built-ins: `${session.outDir}`, `${case.id}`, `${step.index}`, `${timestamp}`.

---

## 8. Durations & types

| Type | Format | Examples |
| --- | --- | --- |
| Duration | number + unit | `500ms`, `2s`, `1m` (bare number = milliseconds) |
| Coordinate | integer (logical px) | `412` |
| Boolean | `true`/`false` (or `yes`/`no` for `expect`) | |
| Glob (filter/tags) | shell-style | `TC-00*`, `smoke` |

---

## 9. Validation rules

The runner rejects a session (exit `2`) when:

- `version` is missing or unsupported.
- `session.application.path` is missing.
- A test case lacks `id`, `name`, `steps`, or `validation`, or `id` is duplicated.
- A step lacks `human` or `machine`.
- A `machine` command lacks `action` or uses an unknown `action`.
- `validation` lacks `human` or a non-empty `assert` list.
- A command is missing required arguments (e.g., `mouse_click` without a point `target`,
  `assert_ai` without `question`).
- `expect` is not `yes`/`no`, `provider` is not a known provider, or a duration is malformed.

`uitest validate <session.yaml>` performs all of the above without launching anything.

---

## 10. Authoring conventions (recommended)

- Keep `id`s stable and ordered (`TC-001`, `TC-002`, …) so reports are easy to scan.
- Write each step's `human` in the imperative voice the way a manual tester would ("Click Save").
- Keep `steps` focused on *acting*; put the pass/fail checks in `validation.assert`.
- Make `assert_ai` questions **specific and binary** ("Is a green success toast visible?")
  rather than open-ended ("Does this look right?").
- Prefer `expect: no` for negative checks instead of phrasing the question in the negative.
- Take a `screenshot` (in a step's `machine` list) before a risky action so failures have
  before/after context.
- Group related cases with `tags` for selective `--filter` runs.

See [examples/TestSessionCases.yaml](../examples/TestSessionCases.yaml) for a complete sample.
