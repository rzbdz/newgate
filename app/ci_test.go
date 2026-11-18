package app

import (
	"path/filepath"
	"testing"

	"github.com/rzbdz/newgate/tools/ciyaml"
)

// TestWorkflowsAreStructurallyValid is the **second line of defense** for `tools/ciyaml`: CI is
// the only place that can run all the tests, and it is itself the file being checked -- once a
// workflow is invalid, GitHub rejects the whole run in 0 seconds and not a single job starts.
// So this check cannot live only in CI (that would be "the fire brigade living in the burning
// house"); it must be able to stop people at local `go test ./...`.
//
// The lesson from 2026-09-20: flattening go/ deleted the `run:` line of one step, and the CI of
// the next six commits went all red, which looked like the tests were broken when in fact the
// tests never ran. The check lives in tools/ciyaml (one implementation), and the distribution's
// ratchet of the same name (testing/ci_test.go) uses the same package.
func TestWorkflowsAreStructurallyValid(t *testing.T) {
	root := filepath.Join(repoDir(t), "..")
	findings, err := ciyaml.Check(root)
	if err != nil {
		t.Fatalf("CI 配置检查跑不起来——判据退化了（workflow 目录挪了？）: %v", err)
	}
	for _, f := range findings {
		t.Errorf("%s", f)
	}
}
