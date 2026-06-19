# Debug Mode — Design & Implementation Spec

## 1. What it does

The GUI gains a second execution mode alongside **Run**: **Debug**.

| Mode | Behaviour |
|------|-----------|
| Run  | Current behaviour — runs selected cases unattended |
| Debug | Pauses before every step, waits for user to Continue / Skip / Replace |

**Replace** triggers a live recording pass: the user performs the real interaction on screen while the framework captures mouse clicks and keystrokes, then writes them back into the session YAML as new machine commands for that step.

---

## 2. User-facing flow

```
┌ Session loaded ──────────────────────────────────────────────────────────────┐
│  ▶ Run   🐞 Debug                    [▼ claude] [Selected: 3 cases] [Go]     │
└──────────────────────────────────────────────────────────────────────────────┘
```

### 2a. Debug execution

When Debug is active, a **Step Inspector** panel slides up from the bottom just before each step runs:

```
┌ Step Inspector ──────────────────────────────────────────────────────────────┐
│ ⏸  VC8-4.2 · step 0/1 · "Type a known last name and run the search"          │
│                                                                               │
│  MACHINE COMMANDS                                                             │
│  ① mouse_click   nameTextBox (UIA)          verify: stable                   │
│  ② type_text     "Smith"                                                      │
│  ③ mouse_click   Search_Button (UIA)        verify: stable                   │
│  ④ wait          6 000 ms                                                     │
│                                                                               │
│   [▶ Execute]    [⏭ Skip]    [⏺ Re-record]                                   │
└──────────────────────────────────────────────────────────────────────────────┘
```

- **Execute** — runs the step as-is, advances.
- **Skip** — marks the step passed-without-running; runner moves on (useful when
  the app is already in the right state and the step would be redundant).
- **Re-record** — arms the recorder; execution holds. User interacts with the app
  directly; every click/keystroke is captured. A live feed shows captured commands.

### 2b. Recording overlay

```
┌ Recording ───────────────────────────────────────────────────────────────────┐
│ ⏺ REC  Interact with the app now.  Clicks and keystrokes are captured.       │
│                                                                               │
│  Captured (3 actions):                                                        │
│  ① mouse_click   nameTextBox (automationId detected)  @ (883, 136)           │
│  ② type_text     "Smith"                                                      │
│  ③ mouse_click   Search_Button  @ (1506, 945)                                 │
│                                                                               │
│   [■ Stop & Save to YAML]    [✗ Discard]                                     │
└──────────────────────────────────────────────────────────────────────────────┘
```

Stop & Save:
- replaces the step's `machine:` array in the source YAML (preserving all other YAML)
- pops a toast: **"Step 0 updated — VC8-4.2.yaml saved"**
- advances to the next step normally

---

## 3. Backend architecture

### 3.1 Runner hook (new)

Add to `runner.Options`:

```go
// BeforeEachStep, if non-nil, is called before each step executes.
// The returned StepVerdict controls what the runner does with the step.
// It may block (step-level debugger pause).
BeforeEachStep func(ctx context.Context, ev StepHookEvent) StepVerdict

type StepHookEvent struct {
    CaseID    string
    Phase     string
    StepIndex int
    Human     string
    Machine   []session.Command // read-only view
}

type StepVerdict int
const (
    VerdictRun  StepVerdict = iota // execute the step
    VerdictSkip                    // skip, count as passed
)
```

Call site in `runner/case.go → runStep`, before the `runStepOnce` loop:

```go
if r.opts.BeforeEachStep != nil {
    ev := runner.StepHookEvent{CaseID: caseID, Phase: phase,
        StepIndex: index, Human: step.Human, Machine: step.Machine}
    if r.opts.BeforeEachStep(ctx, ev) == runner.VerdictSkip {
        return skippedStep(step, phase, index)
    }
}
```

### 3.2 New event types

```go
StepPaused      Type = "step.paused"       // debugger waiting for verdict
RecordingBegan  Type = "recording.began"
RecordingUpdate Type = "recording.update"  // fired on each captured action
RecordingStopped Type = "recording.stopped"
```

`StepPaused` carries the full `StepHookEvent` payload (caseId, phase, stepIndex,
human, machineDesc array) so the UI can render the inspector without a separate fetch.

### 3.3 Debug controller (bridge.go addition)

```go
type debugCtrl struct {
    mu        sync.Mutex
    verdictCh chan StepVerdict // non-nil only while paused at a step
    recCh     chan []RecordedAction // non-nil during recording
}
```

The `BeforeEachStep` closure captures `debugCtrl`. When called:
1. Creates `verdictCh`, pushes `StepPaused` event.
2. Blocks on `verdictCh` or `ctx.Done()`.
3. On `VerdictSkip` — returns immediately.
4. On `VerdictRun` — returns immediately (normal execution).
5. On `VerdictReplace` (special path): creates `recCh`, pushes `RecordingBegan`,
   blocks on `recCh`. When recording stops, calls `patchStepMachine` to save the
   YAML, then returns `VerdictSkip` (interaction already performed by the user).

### 3.4 Input recorder (platform layer)

New interface in `platform/platform.go`:

```go
// RecordInput starts capturing raw mouse clicks and keystrokes.
// Each physical event is delivered to the returned channel.
// Call Stop() to end recording.
RecordInput() (InputRecorder, error)

type RecordedAction struct {
    At     time.Time
    Action string  // "mouse_click" | "type_text" | "key_press"
    X, Y   int     // for mouse_click
    Button string  // "left" | "right" | "middle"
    Count  int     // for double-click etc.
    Text   string  // for type_text (accumulated)
    Keys   string  // for key_press chord
    // Enriched by UIA hit-test after capture:
    UIAID  string  // AutomationId at click point, if found
    UIAName string
}

type InputRecorder interface {
    // C delivers actions as they arrive (buffered channel, cap 256).
    C() <-chan RecordedAction
    Stop()
}
```

Implementation (`win_driver.go`):
- Install `WH_MOUSE_LL` + `WH_KEYBOARD_LL` hooks via `SetWindowsHookExW`.
- On each `WM_LBUTTONDOWN` / `WM_RBUTTONDOWN`, snapshot cursor, call
  `ElementAtPoint` (async, best-effort) to enrich with UIA identity.
- Accumulate `WM_CHAR` / `WM_KEYDOWN` into `type_text` runs; flush to a
  `key_press` when a non-printable key or modifier is seen.
- Deliver enriched `RecordedAction` structs to the channel.

### 3.5 YAML patcher

```go
// patchStepMachine replaces the machine: block of step `stepIdx` inside case
// `caseID` in the YAML file at `path`. Uses yaml.v3 node API to preserve
// comments and formatting everywhere except the replaced block.
func patchStepMachine(path, caseID string, stepIdx int,
    actions []RecordedAction) error
```

Each `RecordedAction` is converted to a `yaml.Node` tree:
- `mouse_click` with UIA → `action: mouse_click / target: { window: … } / uia: { automationId: … } / verify: { stable: true }`
- `mouse_click` without UIA → `action: mouse_click / target: { x: …, y: … } / verify: { stable: true }`
- `type_text` → `action: type_text / text: "…"`
- `key_press` → `action: key_press / keys: "…"`

### 3.6 New HTTP endpoints

| Method | Path | Body | Action |
|--------|------|------|--------|
| POST | `/debug/verdict` | `{"action":"run"\|"skip"\|"replace"}` | Send verdict to blocked `BeforeEachStep` |
| POST | `/debug/record/stop` | `{}` | Stop recording, get actions back |
| POST | `/debug/record/discard` | `{}` | Discard and un-arm; send VerdictSkip |

---

## 4. UI implementation (ui.html)

### 4.1 Mode toggle

Replace the single **Run** button with a pill toggle:

```html
<div id="mode-toggle">
  <button id="btn-run-mode"   class="mode active">▶ Run</button>
  <button id="btn-debug-mode" class="mode">🐞 Debug</button>
</div>
```

`debugMode` JS flag gates whether `BeforeEachStep` events trigger the inspector.
Pass `debugMode: true` in `runOptions` to the `/run` endpoint so the backend
arms the hook only when needed (avoids overhead in normal runs).

### 4.2 Step Inspector panel

Hidden `<div id="step-inspector">` fixed to the bottom of the window.
Appears on `step.paused` event, hides on `step.started` (next step began).

Contents populated from the event payload:
- Header: case id + step index + human label
- Machine list: each command rendered as a one-liner badge
- Three action buttons wired to `POST /debug/verdict`

### 4.3 Recording panel

Replaces the Step Inspector when `recording.began` fires. Shows a live list
updated on every `recording.update`. `■ Stop & Save` POSTs to
`/debug/record/stop`; on success the panel collapses and a toast appears.

---

## 5. Implementation order

| # | Work item | Where |
|---|-----------|-------|
| 1 | `StepHookEvent` / `StepVerdict` types, `BeforeEachStep` hook wired in `runStep` | `runner/case.go`, `runner/runner.go` |
| 2 | New event types (`StepPaused`, `Recording*`) | `event/event.go` |
| 3 | `debugCtrl` in `bridge.go`; `/debug/verdict` endpoint | `bridge.go`, `server.go` |
| 4 | Mode toggle + Step Inspector UI (Execute / Skip wired) | `ui.html` |
| 5 | `RecordInput` interface + Windows low-level hook impl | `platform/platform.go`, `platform/win_driver.go` |
| 6 | Recording overlay UI + `/debug/record/stop` endpoint | `ui.html`, `server.go`, `bridge.go` |
| 7 | `patchStepMachine` YAML writer | `bridge.go` |
| 8 | End-to-end test: debug walk + re-record + verify saved YAML | manual |
