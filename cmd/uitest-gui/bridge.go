package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	webview "github.com/webview/webview_go"
	"gopkg.in/yaml.v3"

	"github.com/felenko/uitest/internal/core/event"
	"github.com/felenko/uitest/internal/core/platform"
	"github.com/felenko/uitest/internal/core/report"
	"github.com/felenko/uitest/internal/core/result"
	"github.com/felenko/uitest/internal/core/runner"
	"github.com/felenko/uitest/internal/core/session"
)

// app holds GUI state and the Go↔JS bridge (docs/04 §4).
type app struct {
	w webview.WebView

	mu          sync.Mutex
	sessionPath string
	outDir      string
	reportPath  string
	cancel      context.CancelFunc
	running     bool
	runGen      int // bumped each startRun; only the latest goroutine clears running
	lastResults *result.Results // last partial results flushed to disk (crash backstop)
	buf         eventBuffer
	debug       *debugCtrl      // non-nil when a debug-mode run is active
	newSessRec  *newSessRecorder // non-nil while recording a new session

	pauseMu sync.Mutex
	paused  bool

	// webviewDone is set to 1 by markWebviewDone once w.Run() returns.
	// evalAsync checks this to skip Dispatch calls to the dead window.
	webviewDone int32
}

// --------------------------------------------------------------------------
// debugCtrl — step-through debugger + recording controller
// --------------------------------------------------------------------------

// undoEntry holds a full YAML snapshot to support undo/redo of step patches.
type undoEntry struct {
	label   string
	path    string
	content []byte
}

// debugCtrl manages the debug-mode state: command-level pausing, input
// recording, and undo/redo for YAML patches. One instance per run; nil when
// not in debug mode.
type debugCtrl struct {
	a   *app
	drv platform.Driver

	mu                sync.Mutex
	verdictCh         chan runner.StepVerdict  // non-nil while paused at a command
	recorder          platform.InputRecorder  // non-nil during recording
	readerDone        chan struct{}            // closed when reader goroutine exits
	capturedActions   []platform.RecordedAction
	skipRemainingCmds bool   // set by Re-record/Delete; cleared at step boundary

	curCaseID  string
	curStepIdx int
	curCmdIdx  int

	undoStack []undoEntry
	redoStack []undoEntry
}

func newDebugCtrl(a *app) *debugCtrl { return &debugCtrl{a: a} }

// beforeCommand is called by the runner before each machine command in debug mode.
func (ctrl *debugCtrl) beforeCommand(ctx context.Context, ev runner.CommandHookEvent) runner.StepVerdict {
	ctrl.mu.Lock()
	if ev.CmdIndex == 0 {
		// Step boundary: reset skip flag so the new step's commands run normally.
		ctrl.skipRemainingCmds = false
		ctrl.curCaseID = ev.CaseID
		ctrl.curStepIdx = ev.StepIndex
	}
	skip := ctrl.skipRemainingCmds
	if !skip {
		ctrl.curCmdIdx = ev.CmdIndex
	}
	ctrl.mu.Unlock()

	if skip {
		return runner.VerdictSkip
	}

	ch := make(chan runner.StepVerdict, 1)
	ctrl.mu.Lock()
	ctrl.verdictCh = ch
	ctrl.mu.Unlock()

	ctrl.a.pushEvent(event.Event{
		Type:        event.CommandPaused,
		CaseID:      ev.CaseID,
		Phase:       ev.Phase,
		StepIndex:   ev.StepIndex,
		Human:       ev.Human,
		CmdIndex:    ev.CmdIndex,
		TotalCmds:   ev.TotalCmds,
		CmdDesc:     runner.DescribeCommand(&ev.Cmd),
		MachineCmds: runner.DescribeMachineList(ev.AllCmds),
	})

	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		return runner.VerdictSkip
	}
}

func (ctrl *debugCtrl) sendVerdict(v runner.StepVerdict) {
	ctrl.mu.Lock()
	ch := ctrl.verdictCh
	ctrl.verdictCh = nil
	ctrl.mu.Unlock()
	if ch != nil {
		select {
		case ch <- v:
		default:
		}
	}
}

func (ctrl *debugCtrl) startRecording() error {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if ctrl.recorder != nil {
		return fmt.Errorf("already recording")
	}
	if ctrl.drv == nil {
		return fmt.Errorf("driver not available")
	}
	rec, err := ctrl.drv.RecordInput()
	if err != nil {
		return fmt.Errorf("start recording: %w", err)
	}
	ctrl.recorder = rec
	ctrl.capturedActions = nil
	ctrl.readerDone = make(chan struct{})
	ctrl.skipRemainingCmds = true // skip remaining cmds in this step while recording

	go func() {
		defer close(ctrl.readerDone)
		for action := range rec.C() {
			ctrl.mu.Lock()
			ctrl.capturedActions = append(ctrl.capturedActions, action)
			n := len(ctrl.capturedActions)
			ctrl.mu.Unlock()
			ctrl.a.pushEvent(event.Event{
				Type:          event.RecordingUpdate,
				RecordedDesc:  action.Describe(),
				RecordedCount: n,
			})
		}
	}()

	ctrl.a.pushEvent(event.Event{Type: event.RecordingBegan})
	return nil
}

// stopRecording ends recording. If discard is false, saves captured actions to YAML.
func (ctrl *debugCtrl) stopRecording(discard bool) error {
	ctrl.mu.Lock()
	rec := ctrl.recorder
	ctrl.recorder = nil
	readerDone := ctrl.readerDone
	ctrl.mu.Unlock()

	if rec == nil {
		return fmt.Errorf("not currently recording")
	}

	rec.Stop()        // pump+process finish; out is closed
	<-readerDone      // reader goroutine collected all actions

	ctrl.mu.Lock()
	actions := make([]platform.RecordedAction, len(ctrl.capturedActions))
	copy(actions, ctrl.capturedActions)
	ctrl.capturedActions = nil
	caseID := ctrl.curCaseID
	stepIdx := ctrl.curStepIdx
	ctrl.mu.Unlock()

	if !discard && len(actions) > 0 {
		ctrl.a.mu.Lock()
		path := ctrl.a.sessionPath
		ctrl.a.mu.Unlock()
		if err := ctrl.patchAndRecord(path, caseID, stepIdx, actions); err != nil {
			return err
		}
	}

	ctrl.sendVerdict(runner.VerdictSkip) // unblock BeforeEachStep

	ctrl.a.pushEvent(event.Event{
		Type:          event.RecordingStopped,
		RecordedCount: len(actions),
		Message:       fmt.Sprintf("%d action(s) captured", len(actions)),
	})
	return nil
}

// patchAndRecord writes actions to the YAML file and pushes an undoEntry.
func (ctrl *debugCtrl) patchAndRecord(path, caseID string, stepIdx int, actions []platform.RecordedAction) error {
	current, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := patchStepMachine(path, caseID, stepIdx, actions); err != nil {
		return err
	}
	label := fmt.Sprintf("%s step %d", caseID, stepIdx)
	ctrl.mu.Lock()
	ctrl.undoStack = append(ctrl.undoStack, undoEntry{label: label, path: path, content: current})
	ctrl.redoStack = nil // new patch clears redo
	ctrl.mu.Unlock()
	ctrl.a.pushEvent(event.Event{
		Type:    event.Log,
		Level:   "info",
		Message: fmt.Sprintf("debug: %s updated and saved to %s", label, filepath.Base(path)),
	})
	return nil
}

func (ctrl *debugCtrl) undo() (string, error) {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.undoStack) == 0 {
		return "", fmt.Errorf("nothing to undo")
	}
	entry := ctrl.undoStack[len(ctrl.undoStack)-1]
	ctrl.undoStack = ctrl.undoStack[:len(ctrl.undoStack)-1]

	current, err := os.ReadFile(entry.path)
	if err != nil {
		return "", err
	}
	ctrl.redoStack = append(ctrl.redoStack, undoEntry{label: entry.label, path: entry.path, content: current})
	if err := os.WriteFile(entry.path, entry.content, 0o644); err != nil {
		return "", err
	}
	return entry.label, nil
}

func (ctrl *debugCtrl) redo() (string, error) {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	if len(ctrl.redoStack) == 0 {
		return "", fmt.Errorf("nothing to redo")
	}
	entry := ctrl.redoStack[len(ctrl.redoStack)-1]
	ctrl.redoStack = ctrl.redoStack[:len(ctrl.redoStack)-1]

	current, err := os.ReadFile(entry.path)
	if err != nil {
		return "", err
	}
	ctrl.undoStack = append(ctrl.undoStack, undoEntry{label: entry.label, path: entry.path, content: current})
	if err := os.WriteFile(entry.path, entry.content, 0o644); err != nil {
		return "", err
	}
	return entry.label, nil
}

func (ctrl *debugCtrl) undoAvailable() bool {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	return len(ctrl.undoStack) > 0
}

func (ctrl *debugCtrl) redoAvailable() bool {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	return len(ctrl.redoStack) > 0
}

// --------------------------------------------------------------------------
// patchStepMachine — YAML writer (preserves comments, only replaces machine:)
// --------------------------------------------------------------------------

// patchStepMachine replaces the machine: block of step stepIdx inside case
// caseID in the YAML file at path. Uses yaml.v3 node API to preserve all
// comments and formatting outside the replaced block.
func patchStepMachine(path, caseID string, stepIdx int, actions []platform.RecordedAction) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty YAML document")
	}
	root := doc.Content[0]

	casesNode := ymGet(root, "testCases")
	if casesNode == nil || casesNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("testCases not found or not a sequence in %s", filepath.Base(path))
	}

	var caseNode *yaml.Node
	for _, c := range casesNode.Content {
		if c.Kind == yaml.MappingNode && ymGetStr(c, "id") == caseID {
			caseNode = c
			break
		}
	}
	if caseNode == nil {
		return fmt.Errorf("case %q not found in %s", caseID, filepath.Base(path))
	}

	stepsNode := ymGet(caseNode, "steps")
	if stepsNode == nil || stepsNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("steps not found in case %q", caseID)
	}
	if stepIdx < 0 || stepIdx >= len(stepsNode.Content) {
		return fmt.Errorf("step %d out of range (case %q has %d steps)", stepIdx, caseID, len(stepsNode.Content))
	}
	stepNode := stepsNode.Content[stepIdx]
	if stepNode.Kind != yaml.MappingNode {
		return fmt.Errorf("step %d is not a mapping node", stepIdx)
	}

	newMachine := actionsToYAML(actions)
	for i := 0; i+1 < len(stepNode.Content); i += 2 {
		if stepNode.Content[i].Value == "machine" {
			stepNode.Content[i+1] = newMachine
			var buf bytes.Buffer
			enc := yaml.NewEncoder(&buf)
			enc.SetIndent(2)
			if err := enc.Encode(doc.Content[0]); err != nil {
				return err
			}
			if err := enc.Close(); err != nil {
				return err
			}
			return os.WriteFile(path, buf.Bytes(), 0o644)
		}
	}
	return fmt.Errorf("machine: key not found in step %d of case %q", stepIdx, caseID)
}

// actionsToYAML converts a slice of RecordedActions to a yaml.Node sequence.
func actionsToYAML(actions []platform.RecordedAction) *yaml.Node {
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, a := range actions {
		var item *yaml.Node
		switch a.Action {
		case "mouse_click":
			if a.UIAID != "" {
				if a.WindowTitle != "" {
					item = yMap(
						"action", ys("mouse_click"),
						"target", yMap("window", ys(a.WindowTitle)),
						"uia", yMap("automationId", ys(a.UIAID)),
						"verify", yMap("stable", yb(true)),
					)
				} else {
					item = yMap(
						"action", ys("mouse_click"),
						"uia", yMap("automationId", ys(a.UIAID)),
						"verify", yMap("stable", yb(true)),
					)
				}
			} else {
				item = yMap(
					"action", ys("mouse_click"),
					"target", yMap("x", yi(a.X), "y", yi(a.Y)),
					"verify", yMap("stable", yb(true)),
				)
			}
		case "type_text":
			item = yMap("action", ys("type_text"), "text", ys(a.Text))
		case "key_press":
			item = yMap("action", ys("key_press"), "keys", ys(a.Keys))
		}
		if item != nil {
			seq.Content = append(seq.Content, item)
		}
	}
	return seq
}

// YAML node helpers.
func ys(s string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: s} }
func yi(n int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(n)}
}
func yb(v bool) *yaml.Node {
	s := "false"
	if v {
		s = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: s}
}
func yMap(kvs ...interface{}) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(kvs); i += 2 {
		n.Content = append(n.Content, ys(kvs[i].(string)), kvs[i+1].(*yaml.Node))
	}
	return n
}
func ymGet(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
func ymGetStr(node *yaml.Node, key string) string {
	n := ymGet(node, key)
	if n == nil {
		return ""
	}
	return n.Value
}

// --------------------------------------------------------------------------
// Debug HTTP handlers
// --------------------------------------------------------------------------

func (a *app) handleDebugVerdict(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Action string `json:"action"` // "run" | "skip" | "replace"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	switch req.Action {
	case "run":
		dbg.sendVerdict(runner.VerdictRun)
	case "skip":
		dbg.sendVerdict(runner.VerdictSkip)
	case "replace":
		if err := dbg.startRecording(); err != nil {
			jsonErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		jsonErr(w, "unknown action: "+req.Action, http.StatusBadRequest)
		return
	}
	jsonOK(w)
}

func (a *app) handleDebugRecordStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	if err := dbg.stopRecording(false); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

func (a *app) handleDebugRecordDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	if err := dbg.stopRecording(true); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

func (a *app) handleDebugUndo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	label, err := dbg.undo()
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "undone": label,
		"canUndo": dbg.undoAvailable(), "canRedo": dbg.redoAvailable(),
	})
}

func (a *app) handleDebugRedo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	label, err := dbg.redo()
	if err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "redone": label,
		"canUndo": dbg.undoAvailable(), "canRedo": dbg.redoAvailable(),
	})
}

func (a *app) handleDebugDeleteStep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		CaseID  string `json:"caseId"`
		StepIdx int    `json:"stepIdx"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, "invalid request body", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	dbg := a.debug
	path := a.sessionPath
	a.mu.Unlock()
	if dbg == nil {
		jsonErr(w, "not in debug mode", http.StatusBadRequest)
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := deleteStep(path, req.CaseID, req.StepIdx); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	label := fmt.Sprintf("%s step %d (deleted)", req.CaseID, req.StepIdx)
	dbg.mu.Lock()
	dbg.undoStack = append(dbg.undoStack, undoEntry{label: label, path: path, content: current})
	dbg.redoStack = nil
	dbg.skipRemainingCmds = true // skip remaining commands so runner moves to next step
	dbg.mu.Unlock()
	// Unblock the command currently paused at beforeCommand.
	dbg.sendVerdict(runner.VerdictSkip)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true, "canUndo": dbg.undoAvailable(), "canRedo": dbg.redoAvailable(),
	})
}

// deleteStep removes step stepIdx from case caseID in the YAML file.
func deleteStep(path, caseID string, stepIdx int) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty YAML document")
	}
	root := doc.Content[0]
	casesNode := ymGet(root, "testCases")
	if casesNode == nil || casesNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("testCases not found")
	}
	var caseNode *yaml.Node
	for _, c := range casesNode.Content {
		if c.Kind == yaml.MappingNode && ymGetStr(c, "id") == caseID {
			caseNode = c
			break
		}
	}
	if caseNode == nil {
		return fmt.Errorf("case %q not found", caseID)
	}
	stepsNode := ymGet(caseNode, "steps")
	if stepsNode == nil || stepsNode.Kind != yaml.SequenceNode {
		return fmt.Errorf("steps not found in case %q", caseID)
	}
	if stepIdx < 0 || stepIdx >= len(stepsNode.Content) {
		return fmt.Errorf("step %d out of range", stepIdx)
	}
	stepsNode.Content = append(stepsNode.Content[:stepIdx], stepsNode.Content[stepIdx+1:]...)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc.Content[0]); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// --------------------------------------------------------------------------
// New-session recorder — creates a fresh test-session YAML by recording
// --------------------------------------------------------------------------

// newSessRecorder captures actions and metadata for authoring a new session.
type newSessRecorder struct {
	name     string
	appPath  string
	provider string
	caseID   string
	caseName string

	drv        platform.Driver
	rec        platform.InputRecorder
	readerDone chan struct{}

	mu      sync.Mutex
	actions []platform.RecordedAction
}

func (a *app) handleNewSessionStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name     string `json:"name"`
		AppPath  string `json:"appPath"`
		Provider string `json:"provider"`
		CaseID   string `json:"caseId"`
		CaseName string `json:"caseName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.CaseID == "" {
		jsonErr(w, "missing required fields (name, caseId)", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		jsonErr(w, "cannot start new-session recording while a run is active", http.StatusBadRequest)
		return
	}
	if a.newSessRec != nil {
		a.mu.Unlock()
		jsonErr(w, "already recording a new session", http.StatusBadRequest)
		return
	}
	a.mu.Unlock()

	drv := platform.New()
	_ = drv.SetDPIAware()
	rec, err := drv.RecordInput()
	if err != nil {
		jsonErr(w, "start recording: "+err.Error(), http.StatusInternalServerError)
		return
	}
	nsr := &newSessRecorder{
		name: req.Name, appPath: req.AppPath, provider: req.Provider,
		caseID: req.CaseID, caseName: req.CaseName,
		drv: drv, rec: rec,
		readerDone: make(chan struct{}),
	}
	go func() {
		defer close(nsr.readerDone)
		for action := range rec.C() {
			nsr.mu.Lock()
			nsr.actions = append(nsr.actions, action)
			n := len(nsr.actions)
			nsr.mu.Unlock()
			a.pushEvent(event.Event{
				Type:          event.RecordingUpdate,
				RecordedDesc:  action.Describe(),
				RecordedCount: n,
			})
		}
	}()

	a.mu.Lock()
	a.newSessRec = nsr
	a.mu.Unlock()

	a.pushEvent(event.Event{Type: event.RecordingBegan})
	jsonOK(w)
}

func (a *app) handleNewSessionSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	a.mu.Lock()
	nsr := a.newSessRec
	a.mu.Unlock()
	if nsr == nil {
		jsonErr(w, "no new-session recording in progress", http.StatusBadRequest)
		return
	}

	nsr.rec.Stop()
	<-nsr.readerDone

	nsr.mu.Lock()
	actions := make([]platform.RecordedAction, len(nsr.actions))
	copy(actions, nsr.actions)
	nsr.mu.Unlock()

	savePath := req.Path
	if savePath == "" {
		home, _ := os.UserHomeDir()
		savePath = filepath.Join(home, "Documents", safeFilename(nsr.name)+".yaml")
	}

	data, err := buildNewSessionYAML(nsr, actions)
	if err != nil {
		jsonErr(w, "generate YAML: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.MkdirAll(filepath.Dir(savePath), 0o755); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.mu.Lock()
	a.newSessRec = nil
	a.mu.Unlock()

	a.pushEvent(event.Event{
		Type:          event.RecordingStopped,
		RecordedCount: len(actions),
		Message:       fmt.Sprintf("session saved: %s", savePath),
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "path": savePath})
}

func (a *app) handleNewSessionDiscard(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.mu.Lock()
	nsr := a.newSessRec
	a.newSessRec = nil
	a.mu.Unlock()
	if nsr != nil {
		nsr.rec.Stop()
		<-nsr.readerDone
	}
	a.pushEvent(event.Event{
		Type:    event.RecordingStopped,
		Message: "new session recording discarded",
	})
	jsonOK(w)
}

// buildNewSessionYAML generates a minimal valid session YAML from recorded actions.
func buildNewSessionYAML(nsr *newSessRecorder, actions []platform.RecordedAction) ([]byte, error) {
	machineNode := actionsToYAML(actions)
	stepNode := yMap(
		"human", ys("Recorded step"),
		"machine", machineNode,
	)
	stepsNode := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{stepNode}}
	caseNode := yMap(
		"id", ys(nsr.caseID),
		"name", ys(nsr.caseName),
		"steps", stepsNode,
	)
	casesNode := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{caseNode}}

	provider := nsr.provider
	if provider == "" {
		provider = "claude"
	}
	docNode := yMap(
		"session", yMap(
			"name", ys(nsr.name),
			"application", yMap("path", ys(nsr.appPath)),
			"ai", yMap("provider", ys(provider)),
		),
		"testCases", casesNode,
	)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(docNode); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == ' ' {
			b.WriteRune('_')
		} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

func jsonOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func newApp(w webview.WebView) *app { return &app{w: w} }

// isRunning reports whether a run is in progress (used by the close guard).
func (a *app) isRunning() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running
}

// bind exposes Go functions to the UI.
func (a *app) bind() {
	for name, fn := range map[string]any{
		"pickSession":   a.pickSession,
		"savePath":      a.savePathDialog,
		"loadSession":   a.loadSession,
		"startRun":      a.startRun,
		"cancelRun":     a.cancelRun,
		"pauseRun":      a.pauseRun,
		"resumeRun":     a.resumeRun,
		"openReport":    a.openReport,
		"openOutputDir": a.openOutputDir,
	} {
		if err := a.w.Bind(name, fn); err != nil {
			a.debugLog(fmt.Sprintf("bind %s failed: %v", name, err))
		}
	}
}

// recoverToError converts a panic in a bound/bridge function into a returned
// error so a bad call can never take down the whole runner process.
func (a *app) recoverToError(where string, err *error) {
	if rec := recover(); rec != nil {
		a.debugLog(fmt.Sprintf("%s panic: %v\n%s", where, rec, debug.Stack()))
		if err != nil {
			*err = fmt.Errorf("internal error in %s: %v", where, rec)
		}
	}
}

// pickSession opens a native open-file dialog and returns the chosen path.
func (a *app) pickSession() (path string, err error) {
	defer a.recoverToError("pickSession", &err)
	return openFileDialog()
}

// savePathDialog opens a native save-as dialog and returns the chosen path.
func (a *app) savePathDialog(defaultName string) (path string, err error) {
	defer a.recoverToError("savePath", &err)
	return saveFileDialog(defaultName)
}

// sessionSummary is sent to the UI after a successful load.
type sessionSummary struct {
	Name     string        `json:"name"`
	App      string        `json:"app"`
	Provider string        `json:"provider"`
	Path     string        `json:"path"`
	Cases    []caseSummary `json:"cases"`
	Totals   totals        `json:"totals"`
}

type caseSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Folder      string   `json:"folder"`
	Tags        []string `json:"tags"`
	Steps       []string `json:"steps"`
	Asserts     []string `json:"asserts"`
}

type totals struct {
	Cases   int `json:"cases"`
	Steps   int `json:"steps"`
	Asserts int `json:"asserts"`
}

// loadSession parses + validates a session and returns its summary.
func (a *app) loadSession(path string) (sum sessionSummary, err error) {
	defer a.recoverToError("loadSession", &err)
	sess, err := session.Load(path)
	if err != nil {
		return sessionSummary{}, err
	}
	a.mu.Lock()
	a.sessionPath = path
	a.mu.Unlock()

	sum = sessionSummary{
		Name:     sess.Session.Name,
		App:      sess.Session.Application.Path,
		Provider: sess.Session.AI.Provider,
		Path:     path,
	}
	for i := range sess.TestCases {
		tc := &sess.TestCases[i]
		cs := caseSummary{ID: tc.ID, Name: tc.Name, Description: tc.Description, Folder: tc.Folder, Tags: tc.Tags}
		for _, st := range tc.Steps {
			cs.Steps = append(cs.Steps, st.Human)
			sum.Totals.Steps++
		}
		for _, as := range tc.Validation.Assert {
			label := as.Human
			if label == "" {
				label = as.Question
			}
			cs.Asserts = append(cs.Asserts, label)
			sum.Totals.Asserts++
		}
		sum.Cases = append(sum.Cases, cs)
		sum.Totals.Cases++
	}
	return sum, nil
}

// runOptions are sent from the UI when starting a run.
type runOptions struct {
	Provider  string   `json:"provider"`
	OutDir    string   `json:"outDir"`
	Filter    string   `json:"filter"`
	FailFast  bool     `json:"failFast"`
	Embed     bool     `json:"embed"`
	Cases     []string `json:"cases"`     // explicit case ids; empty = all
	DebugMode bool     `json:"debugMode"` // pause before each step for step-through debugging
}

// startRun begins execution and streams events to the UI. All heavy work (session
// load, app launch, cases) runs off the webview UI thread so events can flow back.
func (a *app) startRun(optsJSON string) (err error) {
	defer a.recoverToError("startRun", &err)
	a.mu.Lock()
	path := a.sessionPath
	if path == "" {
		a.mu.Unlock()
		return fmt.Errorf("no session loaded")
	}
	oldCancel := a.cancel
	a.runGen++
	gen := a.runGen
	a.running = true
	a.mu.Unlock()

	a.pauseMu.Lock()
	a.paused = false
	a.pauseMu.Unlock()

	a.buf.reset()
	a.debugLog(fmt.Sprintf("startRun gen=%d path=%q opts=%s", gen, path, optsJSON))
	if oldCancel != nil {
		oldCancel()
	}

	go a.runAsync(path, optsJSON, gen)
	return nil
}

func (a *app) runAsync(path, optsJSON string, gen int) {
	defer func() {
		a.mu.Lock()
		if a.runGen == gen {
			a.running = false
		}
		a.mu.Unlock()
		if rec := recover(); rec != nil {
			a.debugLog(fmt.Sprintf("run crashed: %v\n%s", rec, debug.Stack()))
			a.pushEvent(event.Event{
				Type: event.Log, Level: "error",
				Message: fmt.Sprintf("run crashed: %v", rec),
			})
			// Flush a report from the last known results so completed cases
			// are never lost, even when the run goroutine dies unexpectedly.
			a.flushCrashReport(gen, fmt.Sprintf("%v", rec))
			a.evalAsync(`window.uitestRunCrashed()`)
		}
	}()

	// Let a cancelled prior run exit before we start the next one.
	time.Sleep(150 * time.Millisecond)

	a.mu.Lock()
	stale := a.runGen != gen
	a.mu.Unlock()
	if stale {
		return
	}

	a.pushEvent(event.Event{Type: event.Log, Level: "info", Message: "run accepted — loading session…"})

	var o runOptions
	if optsJSON != "" {
		if err := json.Unmarshal([]byte(optsJSON), &o); err != nil {
			a.failRun(fmt.Sprintf("invalid run options: %v", err))
			return
		}
	}

	sess, err := session.Load(path)
	if err != nil {
		a.failRun(fmt.Sprintf("session load failed: %v", err))
		return
	}

	outDir := o.OutDir
	if outDir == "" {
		base := sess.Session.Settings.OutDir
		if base == "" {
			base = session.DefaultOutDir
		}
		outDir = filepath.Join(base, time.Now().Format("20060102-150405"))
	}
	if abs, aerr := filepath.Abs(outDir); aerr == nil {
		outDir = abs
	}

	// Publish outDir early so the crash backstop and /art/ work from the start.
	a.mu.Lock()
	if a.runGen == gen {
		a.outDir = outDir
		a.reportPath = ""
		a.lastResults = nil
	}
	a.mu.Unlock()

	filter := o.Filter
	if len(o.Cases) > 0 {
		filter = "" // explicit GUI selection wins over the filter box
	}

	provider := sess.Session.AI.Provider
	if o.Provider != "" {
		provider = o.Provider
	}

	// Write a skeleton report immediately so report.html/results.json exist on
	// disk from the first second of the run — a killed or crashed runner can
	// no longer leave behind screenshots without any report.
	skeleton := &result.Results{
		Session:       sess.Session.Name,
		Frontend:      "gui",
		RunnerVersion: Version,
		Provider:      provider,
		Application:   sess.Session.Application.Path,
		StartedAt:     time.Now(),
		FinishedAt:    time.Now(),
	}
	if rp, werr := report.WriteAll(outDir, skeleton, o.Embed); werr != nil {
		a.debugLog("initial report write failed: " + werr.Error())
	} else {
		a.mu.Lock()
		if a.runGen == gen {
			a.reportPath = rp
			a.lastResults = skeleton
		}
		a.mu.Unlock()
	}

	// Create the debug controller early so we can wire BeforeEachStep into opts.
	// The driver is set after runner.New (driver lives inside the runner).
	var dbg *debugCtrl
	if o.DebugMode {
		dbg = newDebugCtrl(a)
	}

	opts := runner.Options{
		OutDir:        outDir,
		Provider:      o.Provider,
		Filter:        filter,
		IDs:           o.Cases,
		Frontend:      "gui",
		RunnerVersion: Version,
		BeforeEachCase: func(ctx context.Context) error {
			return a.waitIfPaused(ctx)
		},
		AfterEachCase: func(partial *result.Results) {
			rp, werr := report.WriteAll(outDir, partial, o.Embed)
			if werr != nil {
				a.debugLog("live report write failed: " + werr.Error())
				return
			}
			a.mu.Lock()
			if a.runGen == gen {
				a.reportPath = rp
				a.lastResults = partial
			}
			a.mu.Unlock()
			if a.runGen == gen {
				a.evalAsync(fmt.Sprintf("window.uitestReport(%s,%s,true)", jsStr(rp), jsStr(outDir)))
			}
		},
	}
	if o.FailFast {
		ff := true
		opts.FailFast = &ff
	}
	if dbg != nil {
		opts.BeforeEachCommand = dbg.beforeCommand
	}

	// Pre-create the output directory so we can open events.jsonl before
	// the runner's setupOutput runs (RunStarted fires before that).
	_ = os.MkdirAll(outDir, 0o755)
	var evFile *os.File
	if f, ferr := os.OpenFile(filepath.Join(outDir, "events.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); ferr == nil {
		evFile = f
	}
	defer func() {
		if evFile != nil {
			evFile.Close()
		}
	}()

	bus := event.New()
	bus.Subscribe(func(e event.Event) {
		// A panic here must never propagate into the runner and poison a case.
		defer func() {
			if rec := recover(); rec != nil {
				a.debugLog(fmt.Sprintf("event subscriber panic: %v", rec))
			}
		}()
		a.pushEvent(e)
		if evFile != nil {
			if data, merr := json.Marshal(e); merr == nil {
				_, _ = evFile.Write(append(data, '\n'))
			}
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	a.mu.Lock()
	if a.runGen != gen {
		a.mu.Unlock()
		cancel()
		return
	}
	a.cancel = cancel
	a.mu.Unlock()

	if len(o.Cases) > 0 {
		a.pushEvent(event.Event{
			Type: event.Log, Level: "info",
			Message: fmt.Sprintf("running %d selected case(s)", len(o.Cases)),
		})
	}

	r := runner.New(sess, opts, bus)

	// Wire the driver into the debug controller now that the runner owns it.
	if dbg != nil {
		dbg.drv = r.Driver()
		a.mu.Lock()
		if a.runGen == gen {
			a.debug = dbg
		}
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			if a.debug == dbg {
				a.debug = nil
			}
			a.mu.Unlock()
		}()
	}

	results, _ := r.Run(ctx)
	if results != nil && r.OutDir() != "" {
		if rp, werr := report.WriteAll(r.OutDir(), results, o.Embed); werr == nil {
			a.mu.Lock()
			if a.runGen == gen {
				a.reportPath = rp
			}
			a.mu.Unlock()
			if a.runGen == gen {
				a.evalAsync(fmt.Sprintf("window.uitestReport(%s,%s,false)", jsStr(rp), jsStr(r.OutDir())))
			}
		}
	}
	cancel()
}

func (a *app) failRun(msg string) {
	a.pushEvent(event.Event{Type: event.Log, Level: "error", Message: msg})
	a.evalAsync(fmt.Sprintf("window.uitestRunFailed(%s)", jsStr(msg)))
}

func (a *app) cancelRun() error {
	a.mu.Lock()
	c := a.cancel
	a.mu.Unlock()
	if c != nil {
		c()
	}
	return nil
}

func (a *app) pauseRun() error {
	a.pauseMu.Lock()
	a.paused = true
	a.pauseMu.Unlock()
	return nil
}

func (a *app) resumeRun() error {
	a.pauseMu.Lock()
	a.paused = false
	a.pauseMu.Unlock()
	return nil
}

// waitIfPaused blocks between cases while the run is paused. Returns on resume or ctx cancel.
func (a *app) waitIfPaused(ctx context.Context) error {
	for {
		a.pauseMu.Lock()
		p := a.paused
		a.pauseMu.Unlock()
		if !p {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (a *app) openReport() error {
	a.mu.Lock()
	p := a.reportPath
	a.mu.Unlock()
	if p == "" {
		return fmt.Errorf("no report yet")
	}
	return openFile(p)
}

func (a *app) openOutputDir() error {
	a.mu.Lock()
	p := a.outDir
	a.mu.Unlock()
	if p == "" {
		return fmt.Errorf("no output yet")
	}
	// explorer.exe returns a non-zero exit even on success, so don't Wait.
	return exec.Command("explorer.exe", p).Start()
}

// pushEvent records an event for HTTP polling. Events reach the UI via
// /events?since=N (polled every 200ms) — not via evalAsync/Dispatch, which
// would double-deliver and accumulate unsafe CGO pointers over long runs.
func (a *app) pushEvent(e event.Event) {
	n := a.buf.append(e)
	if e.Type == event.Log {
		a.debugLog(fmt.Sprintf("[%s] %s", e.Level, e.Message))
	} else {
		a.debugLog(fmt.Sprintf("event[%d] %s case=%s", n, e.Type, e.CaseID))
	}
}

// jsStr returns a JSON-quoted string safe to embed in an Eval call.
func jsStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// markWebviewDone records that w.Run() has returned so evalAsync stops
// dispatching to the dead window.
func (a *app) markWebviewDone() { atomic.StoreInt32(&a.webviewDone, 1) }

// waitForRun blocks until no run is active (runner goroutine has exited and
// written its report). Called after w.Run() so the process stays alive long
// enough for the runner to finish even if the window was closed mid-run.
func (a *app) waitForRun() {
	for {
		a.mu.Lock()
		running := a.running
		a.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// flushCrashReport writes a final report from the last partial results after
// the run goroutine panicked, so completed cases are still captured on disk.
func (a *app) flushCrashReport(gen int, reason string) {
	defer func() {
		if rec := recover(); rec != nil {
			a.debugLog(fmt.Sprintf("flushCrashReport panic: %v", rec))
		}
	}()
	a.mu.Lock()
	res := a.lastResults
	outDir := a.outDir
	stale := a.runGen != gen
	a.mu.Unlock()
	if stale || res == nil || outDir == "" {
		return
	}
	res.FinishedAt = time.Now()
	if rp, werr := report.WriteAll(outDir, res, false); werr != nil {
		a.debugLog("crash report write failed: " + werr.Error())
	} else {
		a.debugLog("crash report written after panic (" + reason + "): " + rp)
		a.mu.Lock()
		if a.runGen == gen {
			a.reportPath = rp
		}
		a.mu.Unlock()
	}
}

// evalAsync runs JS on the webview's UI thread. No-ops once the webview is gone.
func (a *app) evalAsync(js string) {
	if atomic.LoadInt32(&a.webviewDone) != 0 {
		return
	}
	a.w.Dispatch(func() {
		defer func() {
			if rec := recover(); rec != nil {
				a.debugLog(fmt.Sprintf("eval panic: %v", rec))
			}
		}()
		a.w.Eval(js)
	})
}

// openFile opens a file (e.g. report.html) in its default handler. rundll32 +
// FileProtocolHandler is the most reliable way to launch the default browser
// for a local .html file from a GUI process.
func openFile(path string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", path).Start()
}

// handleUpdateProvider patches the ai.provider field in the loaded session YAML.
func (a *app) handleUpdateProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Provider string `json:"provider"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Provider == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "missing or invalid provider"})
		return
	}
	a.mu.Lock()
	path := a.sessionPath
	a.mu.Unlock()
	if path == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "no session loaded"})
		return
	}
	if err := patchProvider(path, req.Provider); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// patchProvider updates session.ai.provider in a session YAML file in-place,
// preserving comments and formatting (uses yaml.v3 node API).
func patchProvider(path, provider string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("empty YAML document")
	}
	if err := setYAMLKey(doc.Content[0], []string{"session", "ai", "provider"}, provider); err != nil {
		return err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc.Content[0]); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// setYAMLKey walks a yaml.v3 mapping node and sets the value at the given key path.
func setYAMLKey(node *yaml.Node, keys []string, value string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("expected mapping node at %q", keys[0])
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == keys[0] {
			if len(keys) == 1 {
				node.Content[i+1].Value = value
				return nil
			}
			return setYAMLKey(node.Content[i+1], keys[1:], value)
		}
	}
	return fmt.Errorf("key %q not found", keys[0])
}
