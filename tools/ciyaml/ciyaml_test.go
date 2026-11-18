package ciyaml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write builds a minimal repository: only .github/workflows/<name>.
func write(t *testing.T, name, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return root
}

// good is a **structurally valid** workflow covering both ways of writing a step: `uses:`
// directly on the dash line, and `- name:` + a `run:` indented one level.
const good = `name: CI
on:
  push:
    branches: [main]

jobs:
  static:
    name: static checks
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: go
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod

      - name: unit tests
        run: GOPROXY=off go test ./...
`

func TestAcceptsAValidWorkflow(t *testing.T) {
	got, err := Check(write(t, "ci.yml", good))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(got) != 0 {
		for _, f := range got {
			t.Errorf("a valid file was reported as broken: %s", f)
		}
	}
}

// TestCatchesTheStepThatTookDownCI is a reproduction of the 2026-09-20 incident: while
// flattening go/, a step's `run:` line was deleted along with it, and GitHub rejected the whole
// run in 0 seconds ever since. The check must catch it.
func TestCatchesTheStepThatTookDownCI(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: opencode e2e

      - name: Claude Code end to end
        run: bash mock/e2e_claude.sh
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "has neither run nor uses")
	if !strings.Contains(got[0].String(), "opencode e2e") {
		t.Errorf("should name that step, got: %s", got[0])
	}
	if got[0].Line != 10 {
		t.Errorf("line = %d, want 10：%s", got[0].Line, got[0])
	}
}

// TestCatchesEmptyValues covers the other half of the incident -- the `defaults: run:` deleted
// down to nothing.
func TestCatchesEmptyValues(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  static:
    runs-on: ubuntu-latest
    defaults:
      run:
    steps:
      - name: build
        run:
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	// Both must be reported: the one under `defaults:` (as in the incident) and the one in the
	// step. Missing one of them means the check only recognized position, not shape.
	if len(got) != 2 {
		t.Fatalf("both empty values must be reported, got %d: %v", len(got), got)
	}
	for _, f := range got {
		if !strings.Contains(f.String(), "empty value") {
			t.Errorf("not reported as an empty value: %s", f)
		}
	}
}

func TestCatchesMissingRunsOnAndSteps(t *testing.T) {
	const broken = `name: CI
on: push

jobs:
  static:
    name: has no runs-on
  e2e:
    runs-on: ubuntu-latest
`
	got, err := Check(write(t, "ci.yml", broken))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "has no runs-on", "static")
	assertOne(t, got, "has no steps", "e2e")
}

func TestCatchesNoJobsAndTabs(t *testing.T) {
	got, err := Check(write(t, "ci.yml", "name: CI\non: push\n"))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "no top-level jobs")

	tabbed := "name: CI\non: push\n\njobs:\n\tstatic:\n    runs-on: ubuntu-latest\n"
	got, err = Check(write(t, "ci.yml", tabbed))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	assertOne(t, got, "tab")
}

// TestDegeneratesLoudly: when the check itself breaks (directory moved, no workflows), it must
// **error**, not return "zero problems" -- that looks exactly like "checked, all clean".
func TestDegeneratesLoudly(t *testing.T) {
	if _, err := Check(t.TempDir()); err == nil {
		t.Fatal("with no .github/workflows it must error instead of quietly returning zero findings")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(root); err == nil {
		t.Fatal("an empty directory must error instead of quietly returning zero findings")
	}
}

// assertOne asserts there is exactly one finding containing all the fragments -- which also
// guarantees one issue is not reported as eight.
func assertOne(t *testing.T, got []Finding, fragments ...string) {
	t.Helper()
	hits := 0
	for _, f := range got {
		all := true
		for _, fr := range fragments {
			if !strings.Contains(f.String(), fr) {
				all = false
			}
		}
		if all {
			hits++
		}
	}
	if hits != 1 {
		t.Fatalf("want exactly one finding containing %q, got %d: %v", fragments, hits, got)
	}
}
