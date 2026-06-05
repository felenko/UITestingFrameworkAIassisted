package session

import (
	"encoding/json"
	"testing"
)

// TestGenerateSchemaValid confirms the generated schema is well-formed JSON and
// carries the expected top-level shape.
func TestGenerateSchemaValid(t *testing.T) {
	data, err := GenerateSchema()
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if doc["$schema"] == nil {
		t.Error("schema missing $schema version")
	}
	if doc["$defs"] == nil {
		t.Error("schema missing $defs")
	}
}

// TestSchemaCoversAllActions guards against drift: every action the runner
// accepts (knownActions) must appear in the schema's action enum, and the enum
// must not list anything the runner rejects.
func TestSchemaCoversAllActions(t *testing.T) {
	data, err := GenerateSchema()
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	var doc struct {
		Defs map[string]struct {
			Properties map[string]struct {
				Enum []any `json:"enum"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cmd, ok := doc.Defs["Command"]
	if !ok {
		t.Fatal("schema has no Command definition")
	}
	action, ok := cmd.Properties["action"]
	if !ok {
		t.Fatal("Command has no action property")
	}
	inEnum := make(map[string]bool, len(action.Enum))
	for _, a := range action.Enum {
		s, isStr := a.(string)
		if !isStr {
			t.Errorf("action enum has a non-string value %v", a)
			continue
		}
		inEnum[s] = true
	}
	for a := range knownActions {
		if !inEnum[a] {
			t.Errorf("action %q is accepted by the runner but missing from the schema enum", a)
		}
	}
	for a := range inEnum {
		if !knownActions[a] {
			t.Errorf("action %q is in the schema enum but not accepted by the runner", a)
		}
	}
}
