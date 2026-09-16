package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestStatePreservesModuleTopLevelConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NEWGATE_HOME", dir)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, "state.json")), 0o770); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	original := []byte(`{
  "default_profile": "default",
  "classifier_override": {"provider":"fast","model":"small"}
}`)
	if err := os.WriteFile(path, original, 0o660); err != nil {
		t.Fatal(err)
	}

	state := LoadState()
	if !bytes.Contains(state.ModuleConfig["classifier_override"], []byte(`"fast"`)) {
		t.Fatalf("module field not captured: %s", state.ModuleConfig["classifier_override"])
	}
	if _, leaked := state.ModuleConfig["default_profile"]; leaked {
		t.Fatal("core-owned state field leaked into module config")
	}
	state.Debug = true
	if err := SaveState(state); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(saved, []byte(`"classifier_override"`)) ||
		!bytes.Contains(saved, []byte(`"fast"`)) {
		t.Fatalf("module field lost after state save:\n%s", saved)
	}
}
