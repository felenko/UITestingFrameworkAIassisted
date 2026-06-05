package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMergeCredentials(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, credentialsFileName)
	if err := os.WriteFile(credPath, []byte("username: cred_user\npassword: cred_pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(dir, "session.yaml")
	if err := os.WriteFile(sessionPath, []byte("version: 1\nsession:\n  application:\n    path: C:\\\\app.exe\nvariables:\n  username: session_user\ntestCases:\n  - id: TC-1\n    name: x\n    steps:\n      - human: h\n        machine:\n          action: wait\n          ms: 1ms\n    validation:\n      human: ok\n      assert:\n        - action: assert_ai\n          question: q\n          target: screen\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(sessionPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.Variables["username"] != "session_user" {
		t.Errorf("username = %q, want session_user (session overrides credentials)", s.Variables["username"])
	}
	if s.Variables["password"] != "cred_pass" {
		t.Errorf("password = %q, want cred_pass", s.Variables["password"])
	}
}

func TestMergeCredentialsFromEnvPath(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, "custom-creds.yaml")
	if err := os.WriteFile(credPath, []byte("apiKey: secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UITEST_CREDENTIALS", credPath)

	s := &Session{SourcePath: filepath.Join(dir, "missing-session.yaml")}
	if err := s.mergeCredentials(); err != nil {
		t.Fatal(err)
	}
	if s.Variables["apiKey"] != "secret" {
		t.Errorf("apiKey = %q, want secret", s.Variables["apiKey"])
	}
}
